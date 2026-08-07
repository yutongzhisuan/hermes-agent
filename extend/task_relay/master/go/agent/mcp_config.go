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

// MasterFileConfig is the unified master agent config (MCP + search).
type MasterFileConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers" yaml:"mcpServers"`
	// Servers is an optional alias for mcpServers.
	Servers map[string]MCPServerConfig `json:"servers" yaml:"servers"`
	Search  *SearchConfig              `json:"search" yaml:"search"`
}

// MCPFileConfig is an alias kept for callers that only care about MCP servers.
type MCPFileConfig = MasterFileConfig

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

// LoadMasterConfigFile reads the unified YAML/JSON master config.
func LoadMasterConfigFile(path string) (*MasterFileConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("master config path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master config: %w", err)
	}
	cfg, err := parseMasterConfig(data, filepath.Ext(path))
	if err != nil {
		return nil, err
	}
	expandMasterConfigEnv(cfg)
	normalizeSearchConfig(cfg.Search)
	if err := validateSearchConfig(cfg.Search); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadMCPConfigFile reads a config file and returns MCP-focused view (unified format).
func LoadMCPConfigFile(path string) (*MCPFileConfig, error) {
	return LoadMasterConfigFile(path)
}

func parseMasterConfig(data []byte, ext string) (*MasterFileConfig, error) {
	var cfg MasterFileConfig
	ext = strings.ToLower(ext)
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse master yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse master json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(data, &cfg); err == nil && hasMasterContent(&cfg) {
			return &cfg, nil
		}
		cfg = MasterFileConfig{}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse master config: unsupported format %q", ext)
		}
	}
	if !hasMasterContent(&cfg) {
		return nil, fmt.Errorf("master config has no mcpServers or search section")
	}
	return &cfg, nil
}

func hasMasterContent(cfg *MasterFileConfig) bool {
	return cfg != nil && (len(cfg.MCPServers) > 0 || len(cfg.Servers) > 0 || cfg.Search != nil)
}

// ServersMap returns the effective server map (mcpServers preferred, else servers).
func (c *MasterFileConfig) ServersMap() map[string]MCPServerConfig {
	if c == nil {
		return nil
	}
	if len(c.MCPServers) > 0 {
		return c.MCPServers
	}
	return c.Servers
}

func expandMasterConfigEnv(cfg *MasterFileConfig) {
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
	if cfg.Search != nil {
		cfg.Search.BaseURL = expandEnvRefs(cfg.Search.BaseURL)
		cfg.Search.APIKey = expandEnvRefs(cfg.Search.APIKey)
		cfg.Search.Provider = expandEnvRefs(cfg.Search.Provider)
		cfg.Search.SearchDepth = expandEnvRefs(cfg.Search.SearchDepth)
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
