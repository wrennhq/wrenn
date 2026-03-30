package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync/atomic"
	"time"
)

// CPCertRenewInterval is how often the control plane should renew its client
// certificate. It is set to half the cert TTL so there is always a wide safety
// margin before expiry.
const CPCertRenewInterval = cpCertTTL / 2

const (
	hostCertTTL = 7 * 24 * time.Hour
	cpCertTTL   = 24 * time.Hour
)

// CA holds a parsed certificate authority ready to issue leaf certificates.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  string // PEM-encoded certificate for embedding in register/refresh responses
}

// ParseCA parses PEM-encoded CA certificate and private key strings.
// The cert and key are expected to be ECDSA P-256.
func ParseCA(certPEM, keyPEM string) (*CA, error) {
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA key PEM")
	}
	keyIface, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA private key: %w", err)
	}

	return &CA{Cert: cert, Key: keyIface, PEM: certPEM}, nil
}

// HostCert holds all material returned when issuing a leaf cert for a host agent.
type HostCert struct {
	CertPEM     string
	KeyPEM      string
	Fingerprint string    // hex-encoded SHA-256 of DER bytes, stored in hosts.cert_fingerprint
	ExpiresAt   time.Time // stored in hosts.cert_expires_at
	TLSCert     tls.Certificate
}

// IssueHostCert generates an ECDSA P-256 key pair and issues a 7-day server
// certificate for the host agent. hostID becomes the common name; the host's
// IP address (parsed from hostAddr) is added as an IP SAN so Go's TLS
// stack can verify the connection without disabling hostname checking.
func IssueHostCert(ca *CA, hostID, hostAddr string) (HostCert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return HostCert{}, fmt.Errorf("generate host key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return HostCert{}, err
	}

	now := time.Now()
	expires := now.Add(hostCertTTL)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostID},
		NotBefore:    now.Add(-time.Minute), // small clock-skew tolerance
		NotAfter:     expires,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Extract IP from "ip:port" address; fall back to DNS SAN if not parseable.
	host, _, err := net.SplitHostPort(hostAddr)
	if err != nil {
		host = hostAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return HostCert{}, fmt.Errorf("create host certificate: %w", err)
	}

	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return HostCert{}, fmt.Errorf("marshal host key: %w", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))

	tlsCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return HostCert{}, fmt.Errorf("build TLS certificate: %w", err)
	}

	fp := fmt.Sprintf("%x", sha256.Sum256(derBytes))

	return HostCert{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: fp,
		ExpiresAt:   expires,
		TLSCert:     tlsCert,
	}, nil
}

// IssueCPClientCert generates a short-lived (24h) ECDSA client certificate for
// the control plane to present during mTLS handshakes with host agents.
// Called once at CP startup; the result is embedded into the shared HTTP client.
func IssueCPClientCert(ca *CA) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate CP client key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wrenn-cp"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(cpCertTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create CP client certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal CP client key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// AgentTLSConfigFromPEM returns a tls.Config for the host agent using the
// PEM-encoded CA certificate. This is used on the agent side where only the
// CA certificate (not the private key) is available.
func AgentTLSConfigFromPEM(caCertPEM string, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil
	}
	return &tls.Config{
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		GetCertificate: getCert,
		MinVersion:     tls.VersionTLS13,
	}
}

// CPCertStore provides lock-free read/write access to the control plane's
// current client TLS certificate. It is used with tls.Config.GetClientCertificate
// to enable hot-swap without restarting the HTTP client.
//
// The zero value is not usable; use NewCPCertStore to create one.
type CPCertStore struct {
	ptr atomic.Pointer[tls.Certificate]
	ca  *CA
}

// NewCPCertStore issues an initial CP client certificate from ca and returns a
// store that can renew it in place. Returns an error if the initial issuance fails.
func NewCPCertStore(ca *CA) (*CPCertStore, error) {
	s := &CPCertStore{ca: ca}
	if err := s.Refresh(); err != nil {
		return nil, err
	}
	return s, nil
}

// Refresh issues a fresh CP client certificate and atomically stores it.
// If issuance fails the existing cert is unchanged.
func (s *CPCertStore) Refresh() error {
	cert, err := IssueCPClientCert(s.ca)
	if err != nil {
		return fmt.Errorf("renew CP client certificate: %w", err)
	}
	s.ptr.Store(&cert)
	return nil
}

// GetClientCertificate satisfies tls.Config.GetClientCertificate. It is called
// per-handshake and always returns the most recently stored certificate.
func (s *CPCertStore) GetClientCertificate(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	cert := s.ptr.Load()
	if cert == nil {
		return nil, fmt.Errorf("no CP client certificate available")
	}
	return cert, nil
}

// CPClientTLSConfig returns a tls.Config for the CP's outbound HTTP client.
// It uses certStore.GetClientCertificate so the certificate can be renewed
// without replacing the config or transport.
func CPClientTLSConfig(ca *CA, certStore *CPCertStore) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return &tls.Config{
		RootCAs:               pool,
		GetClientCertificate:  certStore.GetClientCertificate,
		MinVersion:            tls.VersionTLS13,
	}
}

// randomSerial returns a random 128-bit certificate serial number.
func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}
	return serial, nil
}
