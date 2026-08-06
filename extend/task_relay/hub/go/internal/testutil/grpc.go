package testutil

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/grpcserver"
	gohub "github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/hub"
	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
)

const bufConnSize = 1024 * 1024

// StartTestHubGRPC boots an in-memory Hub gRPC server over bufconn.
func StartTestHubGRPC(t *testing.T) (*gohub.Hub, func(context.Context, string) (net.Conn, error)) {
	t.Helper()

	cfg := config.Config{
		Host:        "127.0.0.1",
		GRPCPort:    0,
		DBPath:      filepath.Join(t.TempDir(), "relay.db"),
		JWTSecret:   "secret",
		JWTIssuer:   "xhermes-relay-hub",
		JWTAudience: "task-relay-hub",
	}
	h, err := gohub.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })

	lis := bufconn.Listen(bufConnSize)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcserver.MasterAuthUnaryInterceptor(h.Auth())),
		grpc.StreamInterceptor(grpcserver.MasterAuthStreamInterceptor(h.Auth())),
	)
	pb.RegisterTaskRelayServer(srv, grpcserver.New(h.Router(), h.EventBus(), h.Registry(), h.Delivery(), h.Router().Config()))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	return h, dialer
}

// TestContext returns a timeout-bound context for integration tests.
func TestContext(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), timeout)
}

// NewMasterClient dials the bufconn Hub with optional JWT.
func NewMasterClient(
	t *testing.T,
	ctx context.Context,
	dialer func(context.Context, string) (net.Conn, error),
	masterJWT string,
) *client.Client {
	t.Helper()
	master, err := client.New(ctx, client.Config{
		Addr:      "passthrough:///bufnet",
		MasterJWT: masterJWT,
		ExtraDial: []grpc.DialOption{
			grpc.WithContextDialer(dialer),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = master.Close() })
	return master
}

// IssueMasterJWT issues a master token from the test Hub.
func IssueMasterJWT(t *testing.T, h *gohub.Hub, masterID string) string {
	t.Helper()
	token, err := h.Auth().IssueMasterJWT(masterID, time.Hour)
	require.NoError(t, err)
	return token
}
