package config

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"os"
	"time"
)

// HTTPClient returns a client that trusts the system roots plus the CA named
// by SSL_CERT_FILE. nono's credential proxy terminates TLS with its own CA and
// exports it through that variable, but Go on macOS verifies against the system
// keychain and ignores it, so the CA has to be added explicitly.
func HTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if pool := rootsWithExtraCA(); pool != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func rootsWithExtraCA() *x509.CertPool {
	path := os.Getenv("SSL_CERT_FILE")
	if path == "" {
		return nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		log.Printf("warning: cannot read SSL_CERT_FILE %q: %v", path, err)
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		log.Printf("warning: no certificates found in SSL_CERT_FILE %q", path)
		return nil
	}
	return pool
}
