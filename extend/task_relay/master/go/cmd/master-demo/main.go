package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/infa/task_relay/master/agent"
	"github.com/infa/task_relay/master/client"
)

func main() {
	goal := flag.String("goal", "", "User goal for the Task Relay Master agent")
	hubAddr := flag.String("hub-grpc", envOr("HUB_GRPC_ADDR", ""), "Hub gRPC address host:port")
	masterJWT := flag.String("master-jwt", envOr("MASTER_JWT", ""), "Master JWT for Hub auth")
	sessionID := flag.String("session", "master-demo", "Master session id")
	mode := flag.String("mode", "deep", "Agent mode: deep (DeepAgent) or react (ChatModelAgent ReAct)")
	disableLocalSub := flag.Bool("disable-local-subagents", false, "Disable DeepAgent general-purpose local subagent")
	disableLocalPlanner := flag.Bool("disable-local-planner", false, "Disable built-in local-planner subagent")
	model := flag.String("model", envOr("OPENAI_MODEL", "gpt-4o-mini"), "OpenAI model name")
	baseURL := flag.String("openai-base-url", envOr("OPENAI_BASE_URL", ""), "Optional OpenAI-compatible base URL")
	tlsCA := flag.String("tls-ca", envOr("HUB_TLS_CA", ""), "Hub TLS CA file")
	tlsCert := flag.String("tls-cert", envOr("HUB_TLS_CERT", ""), "Master mTLS client certificate")
	tlsKey := flag.String("tls-key", envOr("HUB_TLS_KEY", ""), "Master mTLS client private key")
	tlsSkipVerify := flag.Bool("tls-skip-hostname-verify", false, "Skip TLS hostname verification")
	enableMetrics := flag.Bool("metrics", false, "Enable Prometheus client metrics")
	metricsAddr := flag.String("metrics-addr", envOr("MASTER_METRICS_ADDR", ""), "Prometheus metrics listen address")
	enableTracing := flag.Bool("tracing", false, "Enable OpenTelemetry gRPC tracing")
	otelEndpoint := flag.String("otel-endpoint", envOr("OTEL_EXPORTER_OTLP_ENDPOINT", ""), "OTLP trace exporter endpoint")
	timeout := flag.Duration("timeout", 2*time.Minute, "Overall execution timeout")
	verbose := flag.Bool("verbose", false, "Print full agent interaction flow to stderr")
	logLevel := flag.String("log-level", "info", "slog level: debug|info|warn|error|off")
	logJSON := flag.Bool("log-json", false, "Emit JSON slog to stderr")
	configPath := flag.String("config", envOr("MASTER_CONFIG", ""), "Unified master YAML/JSON (mcpServers + search)")
	flag.Parse()

	if *goal == "" {
		fmt.Fprintln(os.Stderr, "usage: master-demo -goal \"...\" [-config master.yaml] [-hub-grpc addr] [-master-jwt token] [-verbose] [-log-level info] [-log-json]")
		fmt.Fprintln(os.Stderr, "omit -hub-grpc/-master-jwt to handle the goal locally in this process")
		os.Exit(2)
	}
	if (*hubAddr == "") != (*masterJWT == "") {
		fmt.Fprintln(os.Stderr, "HUB_GRPC_ADDR and MASTER_JWT must both be set for remote mode, or both omitted for local-only mode")
		os.Exit(2)
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	master, err := agent.New(ctx, agent.Config{
		HubAddr:               *hubAddr,
		MasterJWT:             *masterJWT,
		MasterSession:         *sessionID,
		OpenAIAPIKey:          apiKey,
		OpenAIModel:           *model,
		OpenAIBaseURL:         *baseURL,
		Mode:                  agent.Mode(*mode),
		DisableLocalSubAgents: *disableLocalSub,
		DisableLocalPlanner:   *disableLocalPlanner,
		HubTLS: client.TLSConfig{
			CAFile:             *tlsCA,
			CertFile:           *tlsCert,
			KeyFile:            *tlsKey,
			SkipHostnameVerify: *tlsSkipVerify,
		},
		EnableMetrics: *enableMetrics,
		MetricsAddr:   *metricsAddr,
		EnableTracing: *enableTracing,
		OTelEndpoint:  *otelEndpoint,
		ConfigPath:    *configPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init master: %v\n", err)
		os.Exit(1)
	}
	defer master.Close()
	if master.LocalOnly {
		fmt.Fprintln(os.Stderr, "mode: local-only (no Hub / remote workers)")
	} else {
		fmt.Fprintln(os.Stderr, "mode: remote Relay via Hub")
	}
	if *configPath != "" {
		fmt.Fprintf(os.Stderr, "config: %s\n", *configPath)
	}

	opts, err := runOpts(*verbose, *logLevel, *logJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log config: %v\n", err)
		os.Exit(2)
	}
	answer, err := master.Run(ctx, *goal, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run master: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(answer)
}

func runOpts(verbose bool, logLevel string, logJSON bool) ([]agent.RunOption, error) {
	var opts []agent.RunOption
	if verbose {
		opts = append(opts, agent.WithVerbose(os.Stderr))
	}
	if strings.EqualFold(strings.TrimSpace(logLevel), "off") {
		return opts, nil
	}
	logger, err := agent.NewSlogLogger(os.Stderr, logLevel, logJSON)
	if err != nil {
		return nil, err
	}
	opts = append(opts, agent.WithSlog(logger))
	return opts, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
