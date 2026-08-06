package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/config"
)

func TestParseRequiresJWTSecret(t *testing.T) {
	_, err := config.Parse([]string{"--db", "relay.db"})
	require.Error(t, err)
}

func TestParseAcceptsMinimalFlags(t *testing.T) {
	cfg, err := config.Parse([]string{"--jwt-secret", "secret", "--grpc-port", "9091"})
	require.NoError(t, err)
	require.Equal(t, "secret", cfg.JWTSecret)
	require.Equal(t, 9091, cfg.GRPCPort)
}
