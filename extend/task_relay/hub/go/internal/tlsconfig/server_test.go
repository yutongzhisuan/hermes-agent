package tlsconfig_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
)

func TestLoadServerTLSDisabled(t *testing.T) {
	cfg, err := tlsconfig.LoadServerTLS(tlsconfig.Config{})
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestLoadServerTLSRequiresCAForMTLS(t *testing.T) {
	_, err := tlsconfig.LoadServerTLS(tlsconfig.Config{
		CertFile: "server.crt", KeyFile: "server.key", RequireClientCert: true,
	})
	require.Error(t, err)
}
