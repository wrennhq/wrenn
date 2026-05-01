package envdclient

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// envdPort is the default port envd listens on inside the guest.
const envdPort = 49983

// baseURL returns the HTTP base URL for reaching envd at the given host IP.
func baseURL(hostIP string) string {
	return fmt.Sprintf("http://%s:%d", hostIP, envdPort)
}

// newHTTPClient returns an http.Client with a dedicated transport for talking
// to envd. The transport is intentionally separate from http.DefaultTransport
// so that proxy traffic to user services inside the sandbox cannot interfere
// with envd RPC connections (PTY streams, exec, file ops).
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
}

// newStreamingHTTPClient returns an http.Client without an overall timeout,
// for long-lived streaming RPCs (PTY, exec stream) that can run indefinitely.
func newStreamingHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
}
