package channels

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
)

// urlConfigFields are the provider config keys that carry a caller-supplied
// destination URL. Only these can drive an outbound HTTP connection to an
// attacker-chosen host, so they are the SSRF-relevant fields to validate.
var urlConfigFields = []string{"url", "webhook_url", "homeserver_url"}

// validateConfigURLs rejects any destination URL whose host resolves to a
// non-public address. When allowPrivate is true (self-hosted deployments that
// legitimately deliver to internal endpoints) the check is skipped entirely.
func validateConfigURLs(config map[string]string, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	for _, field := range urlConfigFields {
		raw := config[field]
		if raw == "" {
			continue
		}
		if err := validateTargetURL(raw); err != nil {
			return apperr.ValidationFailed.
				Msgf("The %s field must be a reachable public URL: %v", field, err).
				With("field", field)
		}
	}
	return nil
}

// validateTargetURL parses raw as an http(s) URL and rejects it if the host
// resolves to any non-public address. Resolution happens here (parse time) for
// fast user feedback; the dial-time guard (dialControl) re-checks at connect
// time to defeat DNS rebinding.
func validateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}

	// A literal IP needs no DNS lookup.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("host is a non-public address")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("could not resolve host")
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("host resolves to a non-public address")
		}
	}
	return nil
}

// isBlockedIP reports whether ip belongs to a range that must never be a
// notification target: loopback, RFC1918/RFC4193 private, link-local
// (which includes the 169.254.169.254 cloud metadata endpoint), multicast,
// and the unspecified address.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

// dialContext returns a DialContext that blocks connections to non-public
// addresses. The Control hook runs after DNS resolution with the concrete
// resolved IP, so a host that passed parse-time validation but rebinds to a
// private address is still rejected at connect time.
func dialContext(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
				return fmt.Errorf("dial to non-public address %s blocked", ip)
			}
			return nil
		}
	}
	return dialer.DialContext
}
