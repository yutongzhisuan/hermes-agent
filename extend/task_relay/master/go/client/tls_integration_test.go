//go:build integration

package client_test

import (
	"context"
	"os"
	"testing"
	"time"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	defer hub.Close()

	resp, err := hub.DispatchTask(ctx, &pb.TaskSpec{
		TaskId:        "go-mtls-1",
		Goal:          "mtls dispatch",
		CallbackTopic: "go-mtls-topic",
	}, "go-mtls-session", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.GetTaskId() != "go-mtls-1" {
		t.Fatalf("unexpected task id: %s", resp.GetTaskId())
	}
}
