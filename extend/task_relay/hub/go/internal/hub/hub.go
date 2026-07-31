package hub

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/grpcserver"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/wsserver"
	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"google.golang.org/grpc"
)

// Hub is the top-level runtime container for the Go port (P4).
type Hub struct {
	cfg    config.Config
	auth   *auth.Auth
	bus    *eventbus.Bus
	db     *store.SQLite
	router *router.Router
	ws     *wsserver.Server
}

// New opens the configured store and constructs the router.
func New(cfg config.Config) (*Hub, error) {
	db, err := store.OpenSQLite(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	verifier, err := auth.New(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, time.Hour)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	bus := eventbus.New()
	rt := router.NewRouter(db)
	return &Hub{
		cfg:    cfg,
		auth:   verifier,
		bus:    bus,
		db:     db,
		router: rt,
		ws: wsserver.New(wsserver.Deps{
			Router: rt,
			Auth:   verifier,
			Bus:    bus,
		}),
	}, nil
}

// EventBus exposes the in-process watch backbone for tests.
func (h *Hub) EventBus() *eventbus.Bus {
	return h.bus
}

// Auth exposes JWT helpers for tests and tooling.
func (h *Hub) Auth() *auth.Auth {
	return h.auth
}

// Router exposes the task state machine for tests and future WS wiring.
func (h *Hub) Router() *router.Router {
	return h.router
}

// Close releases store resources.
func (h *Hub) Close() error {
	if h.db == nil {
		return nil
	}
	return h.db.Close()
}

// Run serves gRPC and WebSocket JSON-RPC until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) error {
	grpcLis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.GRPCPort))
	if err != nil {
		return fmt.Errorf("listen grpc: %w", err)
	}

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcserver.MasterAuthUnaryInterceptor(h.auth)),
		grpc.StreamInterceptor(grpcserver.MasterAuthStreamInterceptor(h.auth)),
	)
	pb.RegisterTaskRelayServer(srv, grpcserver.New(h.router, h.bus))

	errCh := make(chan error, 2)
	go func() {
		errCh <- srv.Serve(grpcLis)
	}()
	go func() {
		addr := fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.WSPort)
		errCh <- h.ws.ListenAndServe(ctx, addr)
	}()

	select {
	case <-ctx.Done():
		srv.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}
