package tlsconfig_test

import (
	"testing"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
)

func TestLoadServerTLSDisabled(t *testing.T) {
	cfg, err := tlsconfig.LoadServerTLS(tlsconfig.Config{})
	if err != nil || cfg != nil {
		t.Fatalf("expected nil config, got cfg=%v err=%v", cfg, err)
	}
}

func TestLoadServerTLSRequiresCAForMTLS(t *testing.T) {
	_, err := tlsconfig.LoadServerTLS(tlsconfig.Config{
		CertFile: "server.crt", KeyFile: "server.key", RequireClientCert: true,
	})
	if err == nil {
		t.Fatal("expected error when require_client_cert without ca")
	}
}
