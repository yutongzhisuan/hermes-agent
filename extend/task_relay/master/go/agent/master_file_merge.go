package agent

import (
	"maps"
	"strings"
	"time"
)

// HubFileConfig is the hub connection section of master.yaml.
type HubFileConfig struct {
	GRPCAddr string        `json:"grpc_addr" yaml:"grpc_addr"`
	JWT      string        `json:"jwt" yaml:"jwt"`
	Session  string        `json:"session" yaml:"session"`
	TLS      *HubTLSConfig `json:"tls" yaml:"tls"`
}

// HubTLSConfig holds optional mTLS settings for the hub.
type HubTLSConfig struct {
	CAFile             string `json:"ca_file" yaml:"ca_file"`
	CertFile           string `json:"cert_file" yaml:"cert_file"`
	KeyFile            string `json:"key_file" yaml:"key_file"`
	SkipHostnameVerify bool   `json:"skip_hostname_verify" yaml:"skip_hostname_verify"`
}

// OpenAIFileConfig is the LLM section of master.yaml.
type OpenAIFileConfig struct {
	APIKey     string `json:"api_key" yaml:"api_key"`
	Model      string `json:"model" yaml:"model"`
	BaseURL    string `json:"base_url" yaml:"base_url"`
	SmallModel string `json:"small_model" yaml:"small_model"`
}

// AgentFileConfig is the agent behavior section of master.yaml.
type AgentFileConfig struct {
	Mode                  string `json:"mode" yaml:"mode"`
	MaxIterations         int    `json:"max_iterations" yaml:"max_iterations"`
	Instruction           string `json:"instruction" yaml:"instruction"`
	DisableLocalSubAgents bool   `json:"disable_local_subagents" yaml:"disable_local_subagents"`
	DisableLocalPlanner   bool   `json:"disable_local_planner" yaml:"disable_local_planner"`
}

// LogFileConfig controls slog / verbose tracing.
type LogFileConfig struct {
	Level   string `json:"level" yaml:"level"`
	JSON    bool   `json:"json" yaml:"json"`
	Verbose bool   `json:"verbose" yaml:"verbose"`
}

// RuntimeFileConfig holds process runtime settings.
type RuntimeFileConfig struct {
	Timeout string `json:"timeout" yaml:"timeout"`
}

// MetricsFileConfig enables Prometheus metrics export.
type MetricsFileConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Addr    string `json:"addr" yaml:"addr"`
}

// TracingFileConfig enables OpenTelemetry tracing.
type TracingFileConfig struct {
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	OTelEndpoint string `json:"otel_endpoint" yaml:"otel_endpoint"`
}

// FileRuntime is resolved runtime/log settings from the master config file.
type FileRuntime struct {
	Timeout  time.Duration
	LogLevel string
	LogJSON  bool
	Verbose  bool
}

// MergeFileIntoConfig overlays file sections onto cfg (empty cfg fields only).
func MergeFileIntoConfig(cfg Config, file *MasterFileConfig) (Config, FileRuntime, error) {
	var rt FileRuntime
	if file == nil {
		return cfg, defaultFileRuntime(), nil
	}
	rt = resolveFileRuntime(file)
	if file.Hub != nil {
		if cfg.HubAddr == "" {
			cfg.HubAddr = file.Hub.GRPCAddr
		}
		if cfg.MasterJWT == "" {
			cfg.MasterJWT = file.Hub.JWT
		}
		if cfg.MasterSession == "" && file.Hub.Session != "" {
			cfg.MasterSession = file.Hub.Session
		}
		if file.Hub.TLS != nil {
			if cfg.HubTLS.CAFile == "" {
				cfg.HubTLS.CAFile = file.Hub.TLS.CAFile
			}
			if cfg.HubTLS.CertFile == "" {
				cfg.HubTLS.CertFile = file.Hub.TLS.CertFile
			}
			if cfg.HubTLS.KeyFile == "" {
				cfg.HubTLS.KeyFile = file.Hub.TLS.KeyFile
			}
			if !cfg.HubTLS.SkipHostnameVerify {
				cfg.HubTLS.SkipHostnameVerify = file.Hub.TLS.SkipHostnameVerify
			}
		}
	}
	if file.OpenAI != nil {
		if cfg.OpenAIAPIKey == "" {
			cfg.OpenAIAPIKey = file.OpenAI.APIKey
		}
		if cfg.OpenAIModel == "" {
			cfg.OpenAIModel = file.OpenAI.Model
		}
		if cfg.OpenAIBaseURL == "" {
			cfg.OpenAIBaseURL = file.OpenAI.BaseURL
		}
		if cfg.OpenAISmallModel == "" {
			cfg.OpenAISmallModel = file.OpenAI.SmallModel
		}
	}
	if file.Agent != nil {
		if cfg.Mode == "" && file.Agent.Mode != "" {
			cfg.Mode = Mode(file.Agent.Mode)
		}
		if cfg.MaxIterations <= 0 && file.Agent.MaxIterations > 0 {
			cfg.MaxIterations = file.Agent.MaxIterations
		}
		if cfg.Instruction == "" {
			cfg.Instruction = file.Agent.Instruction
		}
		cfg.DisableLocalSubAgents = cfg.DisableLocalSubAgents || file.Agent.DisableLocalSubAgents
		cfg.DisableLocalPlanner = cfg.DisableLocalPlanner || file.Agent.DisableLocalPlanner
	}
	if file.Metrics != nil {
		cfg.EnableMetrics = cfg.EnableMetrics || file.Metrics.Enabled
		if cfg.MetricsAddr == "" {
			cfg.MetricsAddr = file.Metrics.Addr
		}
	}
	if file.Tracing != nil {
		cfg.EnableTracing = cfg.EnableTracing || file.Tracing.Enabled
		if cfg.OTelEndpoint == "" {
			cfg.OTelEndpoint = file.Tracing.OTelEndpoint
		}
	}
	if len(cfg.MCPServers) == 0 {
		cfg.MCPServers = file.ServersMap()
	} else if servers := file.ServersMap(); len(servers) > 0 {
		merged := make(map[string]MCPServerConfig, len(servers)+len(cfg.MCPServers))
		maps.Copy(merged, servers)
		maps.Copy(merged, cfg.MCPServers)
		cfg.MCPServers = merged
	}
	if cfg.Search == nil {
		cfg.Search = file.Search
	}
	if cfg.Exec == nil {
		execCfg, err := execConfigFromFile(file.Exec)
		if err != nil {
			return cfg, rt, err
		}
		cfg.Exec = execCfg
	}
	if cfg.Exec != nil {
		*cfg.Exec = cfg.Exec.WithDefaults()
	}
	if cfg.File == nil {
		cfg.File = fileConfigFromFile(file.File)
	}
	return cfg, rt, nil
}

func defaultFileRuntime() FileRuntime {
	return FileRuntime{Timeout: 2 * time.Minute, LogLevel: "info"}
}

func resolveFileRuntime(file *MasterFileConfig) FileRuntime {
	rt := defaultFileRuntime()
	if file.Log != nil {
		if file.Log.Level != "" {
			rt.LogLevel = file.Log.Level
		}
		rt.LogJSON = file.Log.JSON
		rt.Verbose = file.Log.Verbose
	}
	if file.Runtime != nil && strings.TrimSpace(file.Runtime.Timeout) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(file.Runtime.Timeout))
		if err == nil && d > 0 {
			rt.Timeout = d
		}
	}
	return rt
}
