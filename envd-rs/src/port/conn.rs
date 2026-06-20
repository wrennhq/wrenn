use std::io::{self, BufRead};

#[derive(Debug, Clone)]
pub struct ConnStat {
    pub local_ip: String,
    pub local_port: u32,
    pub status: String,
    pub family: u32,
    pub inode: u64,
}

fn tcp_state_name(hex: &str) -> &'static str {
    match hex {
        "01" => "ESTABLISHED",
        "02" => "SYN_SENT",
        "03" => "SYN_RECV",
        "04" => "FIN_WAIT1",
        "05" => "FIN_WAIT2",
        "06" => "TIME_WAIT",
        "07" => "CLOSE",
        "08" => "CLOSE_WAIT",
        "09" => "LAST_ACK",
        "0A" => "LISTEN",
        "0B" => "CLOSING",
        _ => "UNKNOWN",
    }
}

pub fn read_tcp_connections() -> Vec<ConnStat> {
    let mut conns = Vec::new();
    if let Ok(c) = parse_proc_net_tcp("/proc/net/tcp", libc::AF_INET as u32) {
        conns.extend(c);
    }
    if let Ok(c) = parse_proc_net_tcp("/proc/net/tcp6", libc::AF_INET6 as u32) {
        conns.extend(c);
    }
    conns
}

/// Returns the TCP ports in LISTEN state that are reachable from outside the
/// guest through the host proxy. A port qualifies when it is bound to a
/// wildcard address (`0.0.0.0`/`::`, directly reachable on the TAP interface)
/// or to loopback (`127.0.0.1`/`::1`, bridged to the TAP IP by the socat
/// forwarder). Ports bound to any other specific address are not routable from
/// the host and are excluded, as is `exclude_port` (envd's own control port).
/// The result is deduplicated and sorted ascending.
pub fn reachable_listening_ports(exclude_port: u32) -> Vec<u32> {
    filter_reachable_ports(&read_tcp_connections(), exclude_port)
}

fn filter_reachable_ports(conns: &[ConnStat], exclude_port: u32) -> Vec<u32> {
    let mut ports: Vec<u32> = conns
        .iter()
        .filter(|c| c.status == "LISTEN")
        .filter(|c| is_reachable_bind(&c.local_ip))
        .map(|c| c.local_port)
        .filter(|p| *p != exclude_port)
        .collect();
    ports.sort_unstable();
    ports.dedup();
    ports
}

/// A bind address is reachable from the host when it is a wildcard (directly
/// routed via the TAP interface) or loopback (socat-forwarded to the TAP IP).
fn is_reachable_bind(ip: &str) -> bool {
    matches!(ip, "0.0.0.0" | "::" | "127.0.0.1" | "::1")
}

fn parse_proc_net_tcp(path: &str, family: u32) -> io::Result<Vec<ConnStat>> {
    let file = std::fs::File::open(path)?;
    let reader = io::BufReader::new(file);
    let mut conns = Vec::new();
    let mut first = true;

    for line in reader.lines() {
        let line = line?;
        if first {
            first = false;
            continue;
        }
        let line = line.trim().to_string();
        if line.is_empty() {
            continue;
        }

        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() < 10 {
            continue;
        }

        let (ip, port) = match parse_hex_addr(fields[1], family) {
            Some(v) => v,
            None => continue,
        };

        let state = tcp_state_name(fields[3]);

        let inode: u64 = match fields[9].parse() {
            Ok(v) => v,
            Err(_) => continue,
        };

        conns.push(ConnStat {
            local_ip: ip,
            local_port: port,
            status: state.to_string(),
            family,
            inode,
        });
    }

    Ok(conns)
}

fn parse_hex_addr(s: &str, family: u32) -> Option<(String, u32)> {
    let (ip_hex, port_hex) = s.split_once(':')?;
    let port = u32::from_str_radix(port_hex, 16).ok()?;
    let ip_bytes = hex::decode(ip_hex).ok()?;

    let ip_str = if family == libc::AF_INET as u32 {
        if ip_bytes.len() != 4 {
            return None;
        }
        format!(
            "{}.{}.{}.{}",
            ip_bytes[3], ip_bytes[2], ip_bytes[1], ip_bytes[0]
        )
    } else {
        if ip_bytes.len() != 16 {
            return None;
        }
        let mut octets = [0u8; 16];
        for i in 0..4 {
            octets[i * 4] = ip_bytes[i * 4 + 3];
            octets[i * 4 + 1] = ip_bytes[i * 4 + 2];
            octets[i * 4 + 2] = ip_bytes[i * 4 + 1];
            octets[i * 4 + 3] = ip_bytes[i * 4];
        }
        let addr = std::net::Ipv6Addr::from(octets);
        addr.to_string()
    };

    Some((ip_str, port))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    // tcp_state_name

    #[test]
    fn state_all_known_codes() {
        assert_eq!(tcp_state_name("01"), "ESTABLISHED");
        assert_eq!(tcp_state_name("02"), "SYN_SENT");
        assert_eq!(tcp_state_name("03"), "SYN_RECV");
        assert_eq!(tcp_state_name("04"), "FIN_WAIT1");
        assert_eq!(tcp_state_name("05"), "FIN_WAIT2");
        assert_eq!(tcp_state_name("06"), "TIME_WAIT");
        assert_eq!(tcp_state_name("07"), "CLOSE");
        assert_eq!(tcp_state_name("08"), "CLOSE_WAIT");
        assert_eq!(tcp_state_name("09"), "LAST_ACK");
        assert_eq!(tcp_state_name("0A"), "LISTEN");
        assert_eq!(tcp_state_name("0B"), "CLOSING");
    }

    #[test]
    fn state_unknown_code() {
        assert_eq!(tcp_state_name("FF"), "UNKNOWN");
        assert_eq!(tcp_state_name("00"), "UNKNOWN");
    }

    // parse_hex_addr

    #[test]
    fn ipv4_localhost() {
        let (ip, port) = parse_hex_addr("0100007F:0050", libc::AF_INET as u32).unwrap();
        assert_eq!(ip, "127.0.0.1");
        assert_eq!(port, 80);
    }

    #[test]
    fn ipv4_any() {
        let (ip, port) = parse_hex_addr("00000000:0035", libc::AF_INET as u32).unwrap();
        assert_eq!(ip, "0.0.0.0");
        assert_eq!(port, 53);
    }

    #[test]
    fn ipv4_real_address() {
        // 192.168.1.1 in little-endian = 0101A8C0
        let (ip, port) = parse_hex_addr("0101A8C0:01BB", libc::AF_INET as u32).unwrap();
        assert_eq!(ip, "192.168.1.1");
        assert_eq!(port, 443);
    }

    #[test]
    fn ipv4_wrong_byte_count_returns_none() {
        assert!(parse_hex_addr("0100:0050", libc::AF_INET as u32).is_none());
    }

    #[test]
    fn invalid_hex_returns_none() {
        assert!(parse_hex_addr("ZZZZZZZZ:0050", libc::AF_INET as u32).is_none());
    }

    #[test]
    fn no_colon_returns_none() {
        assert!(parse_hex_addr("0100007F0050", libc::AF_INET as u32).is_none());
    }

    #[test]
    fn ipv6_loopback() {
        // ::1 in /proc/net/tcp6 format: 00000000000000000000000001000000
        let (ip, port) = parse_hex_addr(
            "00000000000000000000000001000000:0035",
            libc::AF_INET6 as u32,
        )
        .unwrap();
        assert_eq!(ip, "::1");
        assert_eq!(port, 53);
    }

    #[test]
    fn ipv6_wrong_byte_count_returns_none() {
        assert!(parse_hex_addr("0100007F:0050", libc::AF_INET6 as u32).is_none());
    }

    // parse_proc_net_tcp

    fn write_tcp_file(content: &str) -> tempfile::NamedTempFile {
        let mut f = tempfile::NamedTempFile::new().unwrap();
        f.write_all(content.as_bytes()).unwrap();
        f.flush().unwrap();
        f
    }

    #[test]
    fn parse_empty_file() {
        let f = write_tcp_file(
            "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n",
        );
        let conns = parse_proc_net_tcp(f.path().to_str().unwrap(), libc::AF_INET as u32).unwrap();
        assert!(conns.is_empty());
    }

    #[test]
    fn parse_single_entry() {
        let content = "\
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 00000000\n";
        let f = write_tcp_file(content);
        let conns = parse_proc_net_tcp(f.path().to_str().unwrap(), libc::AF_INET as u32).unwrap();
        assert_eq!(conns.len(), 1);
        assert_eq!(conns[0].local_ip, "127.0.0.1");
        assert_eq!(conns[0].local_port, 80);
        assert_eq!(conns[0].status, "LISTEN");
        assert_eq!(conns[0].inode, 12345);
        assert_eq!(conns[0].family, libc::AF_INET as u32);
    }

    #[test]
    fn parse_skips_malformed_rows() {
        let content = "\
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 00000000
   bad line
   1: short\n";
        let f = write_tcp_file(content);
        let conns = parse_proc_net_tcp(f.path().to_str().unwrap(), libc::AF_INET as u32).unwrap();
        assert_eq!(conns.len(), 1);
    }

    #[test]
    fn parse_multiple_entries() {
        let content = "\
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 100 1 00000000
   1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 200 1 00000000\n";
        let f = write_tcp_file(content);
        let conns = parse_proc_net_tcp(f.path().to_str().unwrap(), libc::AF_INET as u32).unwrap();
        assert_eq!(conns.len(), 2);
        assert_eq!(conns[0].local_port, 80);
        assert_eq!(conns[1].local_port, 443);
    }

    #[test]
    fn parse_nonexistent_file_errors() {
        assert!(parse_proc_net_tcp("/nonexistent/path", libc::AF_INET as u32).is_err());
    }

    // reachable port filtering

    fn conn(ip: &str, port: u32, status: &str) -> ConnStat {
        ConnStat {
            local_ip: ip.to_string(),
            local_port: port,
            status: status.to_string(),
            family: libc::AF_INET as u32,
            inode: 0,
        }
    }

    #[test]
    fn reachable_bind_accepts_wildcard_and_loopback() {
        assert!(is_reachable_bind("0.0.0.0"));
        assert!(is_reachable_bind("::"));
        assert!(is_reachable_bind("127.0.0.1"));
        assert!(is_reachable_bind("::1"));
    }

    #[test]
    fn reachable_bind_rejects_specific_address() {
        assert!(!is_reachable_bind("192.168.1.5"));
        assert!(!is_reachable_bind("169.254.0.21"));
        assert!(!is_reachable_bind("10.0.0.1"));
    }

    #[test]
    fn filter_keeps_only_listen_state() {
        let conns = vec![
            conn("0.0.0.0", 8000, "LISTEN"),
            conn("0.0.0.0", 9000, "ESTABLISHED"),
        ];
        assert_eq!(filter_reachable_ports(&conns, 49983), vec![8000]);
    }

    #[test]
    fn filter_excludes_unreachable_binds() {
        let conns = vec![
            conn("127.0.0.1", 8000, "LISTEN"),
            conn("169.254.0.21", 8001, "LISTEN"), // socat's own listener
            conn("192.168.1.5", 8002, "LISTEN"),
        ];
        assert_eq!(filter_reachable_ports(&conns, 49983), vec![8000]);
    }

    #[test]
    fn filter_excludes_envd_control_port() {
        let conns = vec![
            conn("0.0.0.0", 49983, "LISTEN"),
            conn("0.0.0.0", 8000, "LISTEN"),
        ];
        assert_eq!(filter_reachable_ports(&conns, 49983), vec![8000]);
    }

    #[test]
    fn filter_dedups_and_sorts() {
        // Same port on IPv4 wildcard and IPv6 loopback collapses to one entry.
        let conns = vec![
            conn("::1", 8000, "LISTEN"),
            conn("0.0.0.0", 8000, "LISTEN"),
            conn("0.0.0.0", 3000, "LISTEN"),
        ];
        assert_eq!(filter_reachable_ports(&conns, 49983), vec![3000, 8000]);
    }

    #[test]
    fn filter_empty_when_no_listeners() {
        let conns = vec![conn("0.0.0.0", 8000, "ESTABLISHED")];
        assert!(filter_reachable_ports(&conns, 49983).is_empty());
    }
}
