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
        format!("{}.{}.{}.{}", ip_bytes[3], ip_bytes[2], ip_bytes[1], ip_bytes[0])
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
