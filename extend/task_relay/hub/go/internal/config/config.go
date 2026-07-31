package config

import (
	"flag"
	"fmt"
)

// Config holds process-level Hub settings for the Go port (P4 scaffold).
type Config struct {
	Host      string
	GRPCPort  int
	WSPort    int
	DBPath    string
	JWTSecret string
}

// Parse reads CLI flags into Config.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("task-relay-hub", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "bind interface")
	grpcPort := fs.Int("grpc-port", 9090, "gRPC master port")
	wsPort := fs.Int("ws-port", 9000, "WebSocket worker port")
	dbPath := fs.String("db", "relay.db", "SQLite path or postgres:// URL")
	jwtSecret := fs.String("jwt-secret", "", "HS256 JWT signing secret (required)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if *jwtSecret == "" {
		return Config{}, fmt.Errorf("--jwt-secret is required")
	}
	return Config{
		Host:      *host,
		GRPCPort:  *grpcPort,
		WSPort:    *wsPort,
		DBPath:    *dbPath,
		JWTSecret: *jwtSecret,
	}, nil
}
