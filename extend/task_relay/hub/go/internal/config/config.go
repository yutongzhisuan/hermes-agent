package config

import (
	"flag"
	"fmt"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/router"
	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/tlsconfig"
)

// Config holds process-level Hub settings for the Go port.
type Config struct {
	Host        string
	GRPCPort    int
	WSPort      int
	MetricsPort int
	DBPath      string
	JWTSecret   string
	JWTIssuer   string
	JWTAudience string
	TLS         tlsconfig.Config
	Router      router.RouterConfig
}

// Parse reads CLI flags into Config.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("task-relay-hub", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "bind interface")
	grpcPort := fs.Int("grpc-port", 9090, "gRPC master port")
	wsPort := fs.Int("ws-port", 9000, "WebSocket worker port")
	metricsPort := fs.Int("metrics-port", 0, "Prometheus metrics HTTP port (0 disables)")
	dbPath := fs.String("db", "relay.db", "SQLite path or postgres:// URL")
	jwtSecret := fs.String("jwt-secret", "", "HS256 JWT signing secret (required)")
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
	routerCfg := router.DefaultRouterConfig()
	routerCfg.PollOfferSeconds = *pollOfferSeconds
	routerCfg.JWTSecret = *jwtSecret
	routerCfg.EncryptInlineContextAtRest = *encryptAtRest
	routerCfg.RequireSignedContextRef = *requireSignedRef
	return Config{
		Host:        *host,
		GRPCPort:    *grpcPort,
		WSPort:      *wsPort,
		MetricsPort: *metricsPort,
		DBPath:      *dbPath,
		JWTSecret:   *jwtSecret,
		JWTIssuer:   "hermes-relay-hub",
		JWTAudience: "task-relay-hub",
		TLS: tlsconfig.Config{
			CertFile: *tlsCert, KeyFile: *tlsKey, CAFile: *tlsCA,
			RequireClientCert: *tlsRequireClient,
		},
		Router: routerCfg,
	}, nil
}
