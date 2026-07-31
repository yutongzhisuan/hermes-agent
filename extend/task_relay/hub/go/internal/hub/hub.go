package hub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/grpcserver"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/orchestrator"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/store"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/wsserver"
	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Hub is the top-level runtime container for the Go port (P4).
type Hub struct {
	cfg        config.Config
	auth       *auth.Auth
	bus        *eventbus.Bus
	store      router.Store
	closeStore func() error
	router     *router.Router
	registry   *registry.Registry
	delivery   *delivery.Coordinator
	ws         *wsserver.Server
}

// New opens the configured store and constructs runtime services.
func New(cfg config.Config) (*Hub, error) {
	st, closeFn, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	verifier, err := auth.New(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, time.Hour)
	if err != nil {
		_ = closeFn()
		return nil, err
	}
	bus := eventbus.New()
	reg := registry.New()
	rt := router.NewRouter(st, registry.NewRouterAdapter(reg), router.DefaultRouterConfig())
	del := delivery.New(rt, reg, wsserver.BuildRunPayload)
	rt.SetOrchestrator(orchestrator.New(st, newEventPublisher(bus)))
	rt.SetOnTaskReady(func(ctx context.Context, taskID string) {
		del.OnTaskPending(ctx, taskID)
	})
	ws := wsserver.New(wsserver.Deps{
		Router: rt, Auth: verifier, Bus: bus, Registry: reg, Delivery: del,
	})
	return &Hub{
		cfg: cfg, auth: verifier, bus: bus, store: st, closeStore: closeFn,
		router: rt, registry: reg, delivery: del, ws: ws,
	}, nil
}

func (h *Hub) EventBus() *eventbus.Bus       { return h.bus }
func (h *Hub) Auth() *auth.Auth              { return h.auth }
func (h *Hub) Router() *router.Router        { return h.router }
func (h *Hub) Registry() *registry.Registry  { return h.registry }
func (h *Hub) Delivery() *delivery.Coordinator { return h.delivery }

func (h *Hub) Close() error {
	if h.closeStore == nil {
		return nil
	}
	return h.closeStore()
}

func (h *Hub) Run(ctx context.Context) error {
	tlsCfg, err := tlsconfig.LoadServerTLS(h.cfg.TLS)
	if err != nil {
		return err
	}
	grpcLis, err := listenWithOptionalTLS("tcp", fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.GRPCPort), tlsCfg)
	if err != nil {
		return fmt.Errorf("listen grpc: %w", err)
	}
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(grpcserver.MasterAuthUnaryInterceptor(h.auth)),
		grpc.StreamInterceptor(grpcserver.MasterAuthStreamInterceptor(h.auth)),
	}
	if tlsCfg != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterTaskRelayServer(srv, grpcserver.New(h.router, h.bus, h.registry, h.delivery))

	errCh := make(chan error, 2)
	go func() { errCh <- srv.Serve(grpcLis) }()
	go func() {
		addr := fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.WSPort)
		errCh <- h.ws.ListenAndServe(ctx, addr, tlsCfg)
	}()
	go h.runTicks(ctx)

	select {
	case <-ctx.Done():
		srv.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func listenWithOptionalTLS(network, addr string, tlsCfg *tls.Config) (net.Listener, error) {
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	if tlsCfg == nil {
		return ln, nil
	}
	return tls.NewListener(ln, tlsCfg), nil
}

func (h *Hub) runTicks(ctx context.Context) {
	cfg := h.router.Config()
	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = h.router.TickTimeouts(ctx)
			h.registry.MarkStale(time.Now().Add(-time.Duration(cfg.WorkerStaleSeconds) * time.Second))
		}
	}
}
