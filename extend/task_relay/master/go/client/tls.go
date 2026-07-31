package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TLSConfig holds client-side TLS/mTLS settings for Hub gRPC connections.
type TLSConfig struct {
	CAFile             string
	CertFile           string
	KeyFile            string
	SkipHostnameVerify bool
}

// Enabled reports whether any TLS setting was configured.
func (c TLSConfig) Enabled() bool {
	return c.CAFile != "" || c.CertFile != "" || c.KeyFile != ""
}

// LoadTransportCredentials builds gRPC transport credentials from TLSConfig.
func LoadTransportCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if !cfg.Enabled() {
		return insecure.NewCredentials(), nil
	}
	if cfg.CertFile != "" && cfg.KeyFile == "" || cfg.CertFile == "" && cfg.KeyFile != "" {
		return nil, fmt.Errorf("tls cert_file and key_file must be provided together")
	}
	if cfg.CertFile != "" && cfg.CAFile == "" {
		return nil, fmt.Errorf("tls ca_file is required when using a client certificate")
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2"},
	}
	if cfg.CAFile != "" {
		caPEM, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read tls ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("parse tls ca_file")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	if cfg.SkipHostnameVerify {
		tlsCfg.InsecureSkipVerify = true
	}
	return credentials.NewTLS(tlsCfg), nil
}
