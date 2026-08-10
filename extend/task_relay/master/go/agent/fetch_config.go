package agent

import "time"

type FetchFileConfig struct {
	Enabled bool                   `json:"enabled" yaml:"enabled"`
	Policy  *FetchPolicyFileConfig `json:"policy" yaml:"policy"`
	Limits  *FetchLimitsFileConfig `json:"limits" yaml:"limits"`
}

type FetchPolicyFileConfig struct {
	DomainAllowList      []string `json:"domain_allow_list" yaml:"domain_allow_list"`
	DomainDenyList       []string `json:"domain_deny_list" yaml:"domain_deny_list"`
	AllowPrivateNetworks bool     `json:"allow_private_networks" yaml:"allow_private_networks"`
}

type FetchLimitsFileConfig struct {
	MaxBytes       int64 `json:"max_bytes" yaml:"max_bytes"`
	TimeoutSeconds int   `json:"timeout_seconds" yaml:"timeout_seconds"`
}

type FetchConfig struct {
	Enabled              bool
	DomainAllowList      []string
	DomainDenyList       []string
	AllowPrivateNetworks bool
	MaxBytes             int64
	Timeout              time.Duration
}

func (c FetchConfig) WithDefaults() FetchConfig {
	if c.MaxBytes <= 0 {
		c.MaxBytes = 1 << 20
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return c
}

func fetchConfigFromFile(f *FetchFileConfig) *FetchConfig {
	if f == nil {
		return nil
	}
	cfg := &FetchConfig{Enabled: f.Enabled}
	if f.Policy != nil {
		cfg.DomainAllowList = f.Policy.DomainAllowList
		cfg.DomainDenyList = f.Policy.DomainDenyList
		cfg.AllowPrivateNetworks = f.Policy.AllowPrivateNetworks
	}
	if f.Limits != nil {
		cfg.MaxBytes = f.Limits.MaxBytes
		if f.Limits.TimeoutSeconds > 0 {
			cfg.Timeout = time.Duration(f.Limits.TimeoutSeconds) * time.Second
		}
	}
	return cfg
}
