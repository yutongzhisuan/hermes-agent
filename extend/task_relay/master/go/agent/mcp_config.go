package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MCPFileConfig is a Cursor/Claude-compatible MCP servers file.
type MCPFileConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers" yaml:"mcpServers"`
	// Servers is an optional alias for mcpServers.
	Servers map[string]MCPServerConfig `json:"servers" yaml:"servers"`
}

// MCPServerConfig describes one MCP server entry.
type MCPServerConfig struct {
	// Type is stdio|sse|http|streamable-http. Empty means infer from command/url.
	Type     string            `json:"type" yaml:"type"`
	Command  string            `json:"command" yaml:"command"`
	Args     []string          `json:"args" yaml:"args"`
	Env      map[string]string `json:"env" yaml:"env"`
	URL      string            `json:"url" yaml:"url"`
	Headers  map[string]string `json:"headers" yaml:"headers"`
	Tools    []string          `json:"tools" yaml:"tools"`
	Disabled bool              `json:"disabled" yaml:"disabled"`
}

var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadMCPConfigFile reads YAML or JSON MCP server definitions from path.
func LoadMCPConfigFile(path string) (*MCPFileConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("mcp config path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mcp config: %w", err)
	}
	cfg, err := parseMCPConfig(data, filepath.Ext(path))
	if err != nil {
		return nil, err
	}
	expandMCPConfigEnv(cfg)
	return cfg, nil
}

func parseMCPConfig(data []byte, ext string) (*MCPFileConfig, error) {
	var cfg MCPFileConfig
	ext = strings.ToLower(ext)
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse mcp yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse mcp json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(data, &cfg); err == nil && hasMCPServers(&cfg) {
			return &cfg, nil
		}
		cfg = MCPFileConfig{}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse mcp config: unsupported format %q", ext)
		}
	}
	if !hasMCPServers(&cfg) {
		return nil, fmt.Errorf("mcp config has no mcpServers entries")
	}
	return &cfg, nil
}

func hasMCPServers(cfg *MCPFileConfig) bool {
	return cfg != nil && (len(cfg.MCPServers) > 0 || len(cfg.Servers) > 0)
}

// ServersMap returns the effective server map (mcpServers preferred, else servers).
func (c *MCPFileConfig) ServersMap() map[string]MCPServerConfig {
	if c == nil {
		return nil
	}
	if len(c.MCPServers) > 0 {
		return c.MCPServers
	}
	return c.Servers
}

func expandMCPConfigEnv(cfg *MCPFileConfig) {
	if cfg == nil {
		return
	}
	for name, srv := range cfg.ServersMap() {
		srv.Command = expandEnvRefs(srv.Command)
		srv.URL = expandEnvRefs(srv.URL)
		for i, a := range srv.Args {
			srv.Args[i] = expandEnvRefs(a)
		}
		for k, v := range srv.Env {
			srv.Env[k] = expandEnvRefs(v)
		}
		for k, v := range srv.Headers {
			srv.Headers[k] = expandEnvRefs(v)
		}
		cfg.ServersMap()[name] = srv
	}
}

func expandEnvRefs(s string) string {
	if s == "" || !strings.Contains(s, "${") {
		return s
	}
	return envRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRefPattern.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		if v, ok := os.LookupEnv(sub[1]); ok {
			return v
		}
		return m
	})
}
