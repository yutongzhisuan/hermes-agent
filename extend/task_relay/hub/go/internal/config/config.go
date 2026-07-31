package config

import (
	"flag"
	"fmt"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/tokenserver"
)

// Config holds process-level Hub settings for the Go port.
type Config struct {
	Host                    string
	GRPCPort                int
	WSPort                  int
	HTTPPort                int
	MetricsPort             int
	DBPath                  string
	JWTSecret               string
	JWTIssuer               string
	JWTAudience             string
	WakeTTLSeconds          int
	EventRetentionDays      int
	WatchStreamBufferEvents int
	BootstrapTokens         map[string]auth.BootstrapEntry
	TLS                     tlsconfig.Config
	Router                  router.RouterConfig
}

// Parse reads CLI flags into Config.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("task-relay-hub", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "bind interface")
	grpcPort := fs.Int("grpc-port", 9090, "gRPC master port")
	wsPort := fs.Int("ws-port", 9000, "WebSocket worker port")
	httpPort := fs.Int("http-port", 9001, "HTTP worker token port")
	metricsPort := fs.Int("metrics-port", 0, "Prometheus metrics HTTP port (0 disables)")
	dbPath := fs.String("db", "relay.db", "SQLite path or postgres:// URL")
	jwtSecret := fs.String("jwt-secret", "", "HS256 JWT signing secret (required)")
	bootstrapTokens := fs.String("bootstrap-tokens", "", "comma token=worker_id[:toolsets:max] or inline JSON")
	wakeTTLSeconds := fs.Int("wake-ttl-seconds", 60, "Mode B wake token TTL (seconds)")
	eventRetentionDays := fs.Int("event-retention-days", 7, "retention window for events/checkpoints/terminal tasks")
	watchStreamBufferEvents := fs.Int("watch-stream-buffer-events", 1024, "bounded WatchTask stream buffer")
	cancelGraceSeconds := fs.Int("cancel-grace-seconds", 60, "cancel grace before hub settles")
	resumeBlobMaxBytes := fs.Int("resume-blob-max-bytes", 1_048_576, "max checkpoint resume_blob bytes")
	pollOfferSeconds := fs.Int("poll-offer-seconds", 30, "two-step poll offer window (seconds)")
	encryptAtRest := fs.Bool("encrypt-inline-context", false, "encrypt inline context at rest")
	requireSignedRef := fs.Bool("require-signed-context-ref", false, "reject unsigned ContextRef dispatches")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	tlsCA := fs.String("tls-ca", "", "TLS client CA file for mTLS")
	tlsRequireClient := fs.Bool("tls-require-client-cert", false, "require client certificate")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if *jwtSecret == "" {
		return Config{}, fmt.Errorf("--jwt-secret is required")
	}
	bootstrap, err := tokenserver.ParseBootstrapTokens(*bootstrapTokens)
	if err != nil {
		return Config{}, fmt.Errorf("bootstrap tokens: %w", err)
	}
	routerCfg := router.DefaultRouterConfig()
	routerCfg.PollOfferSeconds = *pollOfferSeconds
	routerCfg.CancelGraceSeconds = *cancelGraceSeconds
	routerCfg.ResumeBlobMaxBytes = *resumeBlobMaxBytes
	routerCfg.RetentionDays = *eventRetentionDays
	routerCfg.WatchStreamBufferEvents = *watchStreamBufferEvents
	routerCfg.HTTPPort = *httpPort
	routerCfg.JWTSecret = *jwtSecret
	routerCfg.EncryptInlineContextAtRest = *encryptAtRest
	routerCfg.RequireSignedContextRef = *requireSignedRef
	routerCfg.BootstrapTokens = bootstrapToRouter(bootstrap)
	return Config{
		Host:                    *host,
		GRPCPort:                *grpcPort,
		WSPort:                  *wsPort,
		HTTPPort:                *httpPort,
		MetricsPort:             *metricsPort,
		DBPath:                  *dbPath,
		JWTSecret:               *jwtSecret,
		JWTIssuer:               "hermes-relay-hub",
		JWTAudience:             "task-relay-hub",
		WakeTTLSeconds:          *wakeTTLSeconds,
		EventRetentionDays:      *eventRetentionDays,
		WatchStreamBufferEvents: *watchStreamBufferEvents,
		BootstrapTokens:         bootstrap,
		TLS: tlsconfig.Config{
			CertFile: *tlsCert, KeyFile: *tlsKey, CAFile: *tlsCA,
			RequireClientCert: *tlsRequireClient,
		},
		Router: routerCfg,
	}, nil
}

func bootstrapToRouter(entries map[string]auth.BootstrapEntry) map[string]router.BootstrapEntry {
	if len(entries) == 0 {
		return map[string]router.BootstrapEntry{}
	}
	out := make(map[string]router.BootstrapEntry, len(entries))
	for token, entry := range entries {
		out[token] = router.BootstrapEntry{
			WorkerID:        entry.WorkerID,
			AllowedToolsets: append([]string(nil), entry.AllowedToolsets...),
			MaxConcurrent:   entry.MaxConcurrent,
		}
	}
	return out
}
