package hub

import (
	"context"
	"fmt"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
)

// Hub is the top-level runtime container for the Go port (P4).
type Hub struct {
	cfg    config.Config
	router *router.Router
}

// New constructs an unstarted Hub with an in-memory router scaffold.
func New(cfg config.Config) *Hub {
	mem := store.NewMemory()
	return &Hub{
		cfg:    cfg,
		router: router.NewRouter(mem),
	}
}

// Router exposes the task state machine for gRPC/WS layers under development.
func (h *Hub) Router() *router.Router {
	return h.router
}

// Run starts gRPC + WebSocket services until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return fmt.Errorf(
		"go hub transport not wired yet (router scaffold ready; grpc=%s:%d ws=%s:%d db=%s)",
		h.cfg.Host,
		h.cfg.GRPCPort,
		h.cfg.Host,
		h.cfg.WSPort,
		h.cfg.DBPath,
	)
}
