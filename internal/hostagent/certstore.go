package hostagent

import (
	"crypto/tls"
	"fmt"
	"sync/atomic"
)

// CertStore provides lock-free read/write access to the agent's current TLS
// certificate. It is used with tls.Config.GetCertificate to enable hot-swap
// of the agent's cert on JWT refresh without restarting the server.
//
// The zero value is usable; GetCert returns an error until a cert is stored.
type CertStore struct {
	ptr atomic.Pointer[tls.Certificate]
}

// Store atomically replaces the current certificate.
func (s *CertStore) Store(cert *tls.Certificate) {
	s.ptr.Store(cert)
}

// ParseAndStore parses certPEM+keyPEM and atomically replaces the stored cert.
// If parsing fails the existing cert is unchanged.
func (s *CertStore) ParseAndStore(certPEM, keyPEM string) error {
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return fmt.Errorf("parse TLS key pair: %w", err)
	}
	s.ptr.Store(&cert)
	return nil
}

// GetCert satisfies tls.Config.GetCertificate. Returns an error if no cert has
// been stored yet.
func (s *CertStore) GetCert(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := s.ptr.Load()
	if cert == nil {
		return nil, fmt.Errorf("no TLS certificate available")
	}
	return cert, nil
}
