package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Config holds server-side TLS settings. Empty cert/key disables TLS.
type Config struct {
	CertFile          string
	KeyFile           string
	CAFile            string
	RequireClientCert bool
}

// Enabled reports whether server TLS is configured.
func (c Config) Enabled() bool {
	return c.CertFile != "" && c.KeyFile != ""
}

// LoadServerTLS builds a server TLS config or returns nil when disabled.
func LoadServerTLS(cfg Config) (*tls.Config, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if cfg.RequireClientCert && cfg.CAFile == "" {
		return nil, fmt.Errorf("tls require_client_cert requires tls ca_file")
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	if cfg.CAFile != "" {
		pool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = pool
		if cfg.RequireClientCert {
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}
	return tlsCfg, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ca file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("parse ca file")
	}
	return pool, nil
}
