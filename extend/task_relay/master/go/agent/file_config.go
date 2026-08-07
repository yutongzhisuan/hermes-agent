package agent

import (
	"github.com/infa/task_relay/master/agent/policy"
)

type FileToolsFileConfig struct {
	Enabled bool                  `json:"enabled" yaml:"enabled"`
	Root    string                `json:"root" yaml:"root"`
	Policy  *FilePolicyFileConfig `json:"policy" yaml:"policy"`
	Limits  *FileLimitsFileConfig `json:"limits" yaml:"limits"`
}

type FilePolicyFileConfig struct {
	AllowPaths []string `json:"allow_paths" yaml:"allow_paths"`
	DenyPaths  []string `json:"deny_paths" yaml:"deny_paths"`
}

type FileLimitsFileConfig struct {
	MaxReadBytes  int64 `json:"max_read_bytes" yaml:"max_read_bytes"`
	MaxWriteBytes int64 `json:"max_write_bytes" yaml:"max_write_bytes"`
}

type FileToolsConfig struct {
	Enabled       bool
	Root          string
	Policy        policy.PathRules
	MaxReadBytes  int64
	MaxWriteBytes int64
}

func (c FileToolsConfig) WithDefaults(fallbackRoot string) FileToolsConfig {
	if c.Root == "" {
		c.Root = fallbackRoot
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = 1 << 20
	}
	if c.MaxWriteBytes <= 0 {
		c.MaxWriteBytes = 1 << 20
	}
	return c
}

func fileConfigFromFile(f *FileToolsFileConfig) *FileToolsConfig {
	if f == nil {
		return nil
	}
	cfg := &FileToolsConfig{Enabled: f.Enabled, Root: f.Root}
	if f.Policy != nil {
		cfg.Policy = policy.PathRules{AllowList: f.Policy.AllowPaths, DenyList: f.Policy.DenyPaths}
	}
	if f.Limits != nil {
		cfg.MaxReadBytes = f.Limits.MaxReadBytes
		cfg.MaxWriteBytes = f.Limits.MaxWriteBytes
	}
	return cfg
}
