package hub

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	pb "github.com/infa/xhermes-agent/extend/task_relay/gen/go"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/config"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/delivery"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/eventbus"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/grpcserver"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/metrics"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/orchestrator"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/registry"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/runpayload"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/store"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/tokenserver"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/wake"
	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/wsserver"
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
	wake       *wake.Scheduler
	tokens     *tokenserver.Server
	ws         *wsserver.Server
}

// New opens the configured store and constructs runtime services.
func New(cfg config.Config) (*Hub, error) {
	st, closeFn, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	verifier, err := auth.New(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, time.Hour, cfg.BootstrapTokens)
	if err != nil {
		_ = closeFn()
		return nil, err
	}
	bufferSize := cfg.WatchStreamBufferEvents
	if bufferSize <= 0 {
		bufferSize = eventbus.DefaultBufferSize()
	}
	bus := eventbus.New(st, bufferSize)
	reg := registry.New(st)
	if err := reg.LoadFromStore(context.Background()); err != nil {
		_ = closeFn()
		return nil, err
	}
	rt := router.NewRouter(st, registry.NewRouterAdapter(reg), cfg.Router)
	emitter := router.NewBusEmitter(bus)
	rt.SetEmitter(emitter)
	runBuilder := &runpayload.Builder{
		Store: st, DecryptSecret: cfg.JWTSecret, EncryptAtRest: cfg.Router.EncryptInlineContextAtRest,
	}
	buildRun := func(ctx context.Context, claimed router.ClaimedTask) (map[string]any, error) {
		run, err := runBuilder.Build(ctx, claimed)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run": run}, nil
	}
	wsScheme := "ws"
	if cfg.TLS.CertFile != "" {
		wsScheme = "wss"
	}
	relayWSURL := fmt.Sprintf("%s://%s:%d", wsScheme, cfg.Host, cfg.WSPort)
	wakeScheduler := wake.New(reg, verifier.SecretBytes(), relayWSURL, cfg.WakeTTLSeconds)
	del := delivery.New(rt, reg, wakeScheduler, buildRun)
	rt.SetOrchestrator(orchestrator.New(st, newEventPublisher(emitter)))
	rt.SetOnTaskReady(func(ctx context.Context, taskID string) {
		del.OnTaskPending(ctx, taskID)
	})
	rt.SetOnTaskTerminal(func(ctx context.Context, taskID, workerID string) {
		del.OnTaskTerminal(ctx, taskID, workerID)
	})
	tokenSrv := tokenserver.New(verifier)
	ws := wsserver.New(wsserver.Deps{
		Router: rt, Auth: verifier, Registry: reg, Delivery: del,
		RunBuilder: runBuilder, Wake: wakeScheduler,
		ResumeBlobMaxBytes: cfg.Router.ResumeBlobMaxBytes,
	})
	return &Hub{
		cfg: cfg, auth: verifier, bus: bus, store: st, closeStore: closeFn,
		router: rt, registry: reg, delivery: del, wake: wakeScheduler,
		tokens: tokenSrv, ws: ws,
	}, nil
}

func (h *Hub) EventBus() *eventbus.Bus         { return h.bus }
func (h *Hub) Auth() *auth.Auth                { return h.auth }
func (h *Hub) Router() *router.Router          { return h.router }
func (h *Hub) Registry() *registry.Registry    { return h.registry }
func (h *Hub) Delivery() *delivery.Coordinator { return h.delivery }
func (h *Hub) Config() config.Config          { return h.cfg }

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
	pb.RegisterTaskRelayServer(srv, grpcserver.New(h.router, h.bus, h.registry, h.delivery, h.cfg.Router))

	errCh := make(chan error, 4)
	go func() { errCh <- srv.Serve(grpcLis) }()
	go func() {
		addr := fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.WSPort)
		errCh <- h.ws.ListenAndServe(ctx, addr, tlsCfg)
	}()
	go func() {
		addr := fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.HTTPPort)
		errCh <- h.tokens.ListenAndServe(ctx, addr, tlsCfg)
	}()
	if h.cfg.MetricsPort > 0 {
		go func() {
			addr := fmt.Sprintf("%s:%d", h.cfg.Host, h.cfg.MetricsPort)
			errCh <- metrics.ListenAndServe(ctx, addr)
		}()
	}
	go h.runTicks(ctx)

	select {
	case <-ctx.Done():
		srv.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func listenWithOptionalTLS(network, addr string, _ *tls.Config) (net.Listener, error) {
	return net.Listen(network, addr)
}

func (h *Hub) runTicks(ctx context.Context) {
	ticker := time.NewTicker(h.router.Config().TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = h.router.TickTimeouts(ctx)
			_ = h.router.MaybePruneEvents(ctx)
			deadline := time.Now().Add(-time.Duration(h.router.Config().WorkerStaleSeconds) * time.Second)
			h.registry.MarkStale(ctx, deadline)
		}
	}
}
