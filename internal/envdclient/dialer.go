package envdclient

import (
	"fmt"
	"net/http"
)

// envdPort is the default port envd listens on inside the guest.
const envdPort = 49983

// baseURL returns the HTTP base URL for reaching envd at the given host IP.
func baseURL(hostIP string) string {
	return fmt.Sprintf("http://%s:%d", hostIP, envdPort)
}

// newHTTPClient returns an http.Client suitable for talking to envd.
// No special transport is needed — envd is reachable via the host IP
// through the veth/TAP network path.
func newHTTPClient() *http.Client {
	return &http.Client{}
}
