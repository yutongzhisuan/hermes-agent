package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/infa/task_relay/master/agent/policy"
)

type ExecFileConfig struct {
	Enabled        bool                  `json:"enabled" yaml:"enabled"`
	DefaultBackend string                `json:"default_backend" yaml:"default_backend"`
	Policy         *ExecPolicyFileConfig `json:"policy" yaml:"policy"`
	Limits         *ExecLimitsFileConfig `json:"limits" yaml:"limits"`
	Audit          *ExecAuditFileConfig  `json:"audit" yaml:"audit"`
}

type ExecPolicyFileConfig struct {
	Mode         string   `json:"mode" yaml:"mode"`
	AllowList    []string `json:"allow_list" yaml:"allow_list"`
	DenyList     []string `json:"deny_list" yaml:"deny_list"`
	ApprovalList []string `json:"approval_list" yaml:"approval_list"`
	EnvAllowKeys []string `json:"env_allow_keys" yaml:"env_allow_keys"`
}

type ExecLimitsFileConfig struct {
	TimeoutDefault string `json:"timeout_default" yaml:"timeout_default"`
	TimeoutMax     string `json:"timeout_max" yaml:"timeout_max"`
	MaxOutputBytes int64  `json:"max_output_bytes" yaml:"max_output_bytes"`
}

type ExecAuditFileConfig struct {
	Path string `json:"path" yaml:"path"`
}

type ExecConfig struct {
	Enabled        bool
	DefaultBackend string
	Policy         policy.Rules
	EnvAllowKeys   []string
	Limits         ExecLimits
	AuditPath      string
}

func (c ExecConfig) WithDefaults() ExecConfig {
	if c.Limits.TimeoutDefault <= 0 {
		c.Limits.TimeoutDefault = 60 * time.Second
	}
	if c.Limits.TimeoutMax <= 0 {
		c.Limits.TimeoutMax = 10 * time.Minute
	}
	if c.Limits.MaxOutputBytes <= 0 {
		c.Limits.MaxOutputBytes = 1 << 20
	}
	if c.AuditPath == "" {
		home, _ := os.UserHomeDir()
		c.AuditPath = filepath.Join(home, ".task-relay", "exec-audit.jsonl")
	}
	if c.DefaultBackend == "" {
		c.DefaultBackend = "local"
	}
	return c
}

func execConfigFromFile(f *ExecFileConfig) (*ExecConfig, error) {
	if f == nil {
		return nil, nil
	}
	cfg := &ExecConfig{Enabled: f.Enabled, DefaultBackend: f.DefaultBackend}
	if f.Policy != nil {
		cfg.Policy = policy.Rules{
			Mode:         policy.Mode(f.Policy.Mode),
			AllowList:    f.Policy.AllowList,
			DenyList:     f.Policy.DenyList,
			ApprovalList: f.Policy.ApprovalList,
		}
		cfg.EnvAllowKeys = f.Policy.EnvAllowKeys
	}
	if f.Limits != nil {
		if f.Limits.TimeoutDefault != "" {
			d, err := time.ParseDuration(f.Limits.TimeoutDefault)
			if err != nil {
				return nil, fmt.Errorf("exec.limits.timeout_default %q: %w", f.Limits.TimeoutDefault, err)
			}
			cfg.Limits.TimeoutDefault = d
		}
		if f.Limits.TimeoutMax != "" {
			d, err := time.ParseDuration(f.Limits.TimeoutMax)
			if err != nil {
				return nil, fmt.Errorf("exec.limits.timeout_max %q: %w", f.Limits.TimeoutMax, err)
			}
			cfg.Limits.TimeoutMax = d
		}
		cfg.Limits.MaxOutputBytes = f.Limits.MaxOutputBytes
	}
	if f.Audit != nil {
		cfg.AuditPath = f.Audit.Path
	}
	return cfg, nil
}
