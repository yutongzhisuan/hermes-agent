package config_test

import (
	"testing"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/config"
)

func TestParseRequiresJWTSecret(t *testing.T) {
	_, err := config.Parse([]string{"--db", "relay.db"})
	if err == nil {
		t.Fatal("expected error when jwt-secret missing")
	}
}

func TestParseAcceptsMinimalFlags(t *testing.T) {
	cfg, err := config.Parse([]string{"--jwt-secret", "secret", "--grpc-port", "9091"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.JWTSecret != "secret" || cfg.GRPCPort != 9091 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
