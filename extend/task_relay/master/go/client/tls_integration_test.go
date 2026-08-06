//go:build integration

package client_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/client"
	"github.com/infa/task_relay/master/internal/testutil"
	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
)

func TestGoMasterMTLSDispatch(t *testing.T) {
	addr := os.Getenv("HUB_GRPC_ADDR")
	jwt := os.Getenv("MASTER_JWT")
	ca := os.Getenv("HUB_TLS_CA")
	cert := os.Getenv("HUB_TLS_CERT")
	key := os.Getenv("HUB_TLS_KEY")
	if addr == "" || jwt == "" || ca == "" || cert == "" || key == "" {
		t.Skip("HUB_GRPC_ADDR, MASTER_JWT, HUB_TLS_* must be set (run scripts/run_go_master_mtls_e2e.sh)")
	}

	ctx, cancel := testutil.TestContext(t, 30*time.Second)
	defer cancel()

	hub, err := client.New(ctx, client.Config{
		Addr:      addr,
		MasterJWT: jwt,
		TLS: client.TLSConfig{
			CAFile:             ca,
			CertFile:           cert,
			KeyFile:            key,
			SkipHostnameVerify: true,
		},
	})
	require.NoError(t, err)
	defer hub.Close()

	resp, err := hub.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "go-mtls-1",
		Goal:          "mtls dispatch",
		CallbackTopic: "go-mtls-topic",
	}, "go-mtls-session", false)
	require.NoError(t, err)
	require.Equal(t, "go-mtls-1", resp.GetTaskId())
}
