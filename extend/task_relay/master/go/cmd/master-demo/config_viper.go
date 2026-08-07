package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/infa/task_relay/master/agent"
)

// setupViper binds Cobra flags and environment variables.
// Precedence (Viper default): flag > env > config file > default.
func setupViper(cmd *cobra.Command) error {
	viper.SetEnvPrefix("MASTER")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()
	if err := viper.BindPFlags(cmd.Flags()); err != nil {
		return fmt.Errorf("bind flags: %w", err)
	}
	// Conventional env aliases (with and without MASTER_ prefix).
	bindEnvs := []struct {
		key string
		env []string
	}{
		{"config", []string{"MASTER_CONFIG"}},
		{"goal", []string{"MASTER_GOAL"}},
		{"log-level", []string{"MASTER_LOG_LEVEL"}},
		{"verbose", []string{"MASTER_VERBOSE"}},
		{"hub.grpc_addr", []string{"HUB_GRPC_ADDR", "MASTER_HUB_GRPC_ADDR"}},
		{"hub.jwt", []string{"MASTER_JWT", "MASTER_HUB_JWT"}},
		{"hub.session", []string{"MASTER_SESSION"}},
		{"openai.api_key", []string{"OPENAI_API_KEY", "MASTER_OPENAI_API_KEY"}},
		{"openai.model", []string{"OPENAI_MODEL", "MASTER_OPENAI_MODEL"}},
		{"openai.base_url", []string{"OPENAI_BASE_URL", "MASTER_OPENAI_BASE_URL"}},
		{"log.level", []string{"MASTER_LOG_LEVEL"}},
		{"log.json", []string{"MASTER_LOG_JSON"}},
		{"log.verbose", []string{"MASTER_VERBOSE"}},
		{"runtime.timeout", []string{"MASTER_TIMEOUT"}},
		{"search.api_key", []string{"GATEWAY_API_KEY", "TAVILY_API_KEY", "MASTER_SEARCH_API_KEY"}},
		{"search.base_url", []string{"SEARCH_BASE_URL", "TAVILY_BASE_URL", "MASTER_SEARCH_BASE_URL"}},
		{"search.provider", []string{"MASTER_SEARCH_PROVIDER"}},
		{"search.search_backend", []string{"MASTER_SEARCH_SEARCH_BACKEND", "SEARCH_BACKEND"}},
		{"search.extract_backend", []string{"MASTER_SEARCH_EXTRACT_BACKEND", "SEARCH_EXTRACT_BACKEND"}},
		{"search.backend", []string{"MASTER_WEB_BACKEND", "WEB_BACKEND"}},
		{"search.enabled", []string{"MASTER_SEARCH_ENABLED"}},
	}
	for _, b := range providerEnvBindings() {
		bindEnvs = append(bindEnvs, b)
	}
	for _, b := range bindEnvs {
		if err := viper.BindEnv(append([]string{b.key}, b.env...)...); err != nil {
			return fmt.Errorf("bind env %s: %w", b.key, err)
		}
	}
	return nil
}

func providerEnvBindings() []struct {
	key string
	env []string
} {
	providers := []string{"firecrawl", "parallel", "tavily", "perplexity", "gateway", "exa", "searxng", "brave-free", "ddgs"}
	var bindings []struct {
		key string
		env []string
	}
	for _, p := range providers {
		suffix := strings.ReplaceAll(strings.ToUpper(p), "-", "_")
		bindings = append(bindings,
			struct {
				key string
				env []string
			}{
				key: fmt.Sprintf("search.providers.%s.base_url", p),
				env: []string{
					fmt.Sprintf("MASTER_SEARCH_PROVIDERS_%s_BASE_URL", suffix),
				},
			},
			struct {
				key string
				env []string
			}{
				key: fmt.Sprintf("search.providers.%s.api_key", p),
				env: []string{
					fmt.Sprintf("MASTER_SEARCH_PROVIDERS_%s_API_KEY", suffix),
				},
			},
		)
	}
	return bindings
}

func loadMasterFromViper(cmd *cobra.Command) (*agent.MasterFileConfig, agent.Config, agent.FileRuntime, error) {
	configPath := strings.TrimSpace(viper.GetString("config"))
	if configPath == "" {
		return nil, agent.Config{}, agent.FileRuntime{}, fmt.Errorf("config is required (--config/-c or MASTER_CONFIG)")
	}

	fileCfg, err := agent.LoadMasterConfigFile(configPath)
	if err != nil {
		return nil, agent.Config{}, agent.FileRuntime{}, err
	}
	applyViperToFile(fileCfg)

	cfg, rt, err := agent.MergeFileIntoConfig(agent.Config{}, fileCfg)
	if err != nil {
		return nil, agent.Config{}, agent.FileRuntime{}, err
	}
	applyViperToRuntime(cmd, &rt)
	return fileCfg, cfg, rt, nil
}

func applyViperToFile(file *agent.MasterFileConfig) {
	if file == nil {
		return
	}
	if viper.IsSet("hub.grpc_addr") || viper.IsSet("hub.jwt") || viper.IsSet("hub.session") {
		if file.Hub == nil {
			file.Hub = &agent.HubFileConfig{}
		}
		if viper.IsSet("hub.grpc_addr") {
			file.Hub.GRPCAddr = viper.GetString("hub.grpc_addr")
		}
		if viper.IsSet("hub.jwt") {
			file.Hub.JWT = viper.GetString("hub.jwt")
		}
		if v := viper.GetString("hub.session"); viper.IsSet("hub.session") && v != "" {
			file.Hub.Session = v
		}
	}
	if viper.IsSet("openai.api_key") || viper.IsSet("openai.model") || viper.IsSet("openai.base_url") {
		if file.OpenAI == nil {
			file.OpenAI = &agent.OpenAIFileConfig{}
		}
		if viper.IsSet("openai.api_key") {
			file.OpenAI.APIKey = viper.GetString("openai.api_key")
		}
		if v := viper.GetString("openai.model"); viper.IsSet("openai.model") && v != "" {
			file.OpenAI.Model = v
		}
		if viper.IsSet("openai.base_url") {
			file.OpenAI.BaseURL = viper.GetString("openai.base_url")
		}
	}
	if file.Search != nil {
		if viper.IsSet("search.api_key") {
			file.Search.APIKey = viper.GetString("search.api_key")
		}
		if viper.IsSet("search.base_url") {
			file.Search.BaseURL = viper.GetString("search.base_url")
		}
		if v := viper.GetString("search.provider"); viper.IsSet("search.provider") && v != "" {
			file.Search.Provider = v
		}
		if v := viper.GetString("search.search_backend"); viper.IsSet("search.search_backend") && v != "" {
			file.Search.SearchBackend = v
		}
		if v := viper.GetString("search.extract_backend"); viper.IsSet("search.extract_backend") && v != "" {
			file.Search.ExtractBackend = v
		}
		if v := viper.GetString("search.backend"); viper.IsSet("search.backend") && v != "" {
			file.Search.Backend = v
		}
		if viper.IsSet("search.enabled") {
			enabled := viper.GetBool("search.enabled")
			file.Search.Enabled = &enabled
		}
		for _, p := range []string{"firecrawl", "parallel", "tavily", "perplexity", "gateway", "exa", "searxng", "brave-free", "ddgs"} {
			applyViperToProvider(file.Search, p)
		}
	}
	if viper.IsSet("log.level") || viper.IsSet("log.json") || viper.IsSet("log.verbose") {
		if file.Log == nil {
			file.Log = &agent.LogFileConfig{}
		}
		if v := viper.GetString("log.level"); viper.IsSet("log.level") && v != "" {
			file.Log.Level = v
		}
		if viper.IsSet("log.json") {
			file.Log.JSON = viper.GetBool("log.json")
		}
		if viper.IsSet("log.verbose") {
			file.Log.Verbose = viper.GetBool("log.verbose")
		}
	}
	if v := viper.GetString("runtime.timeout"); viper.IsSet("runtime.timeout") && v != "" {
		if file.Runtime == nil {
			file.Runtime = &agent.RuntimeFileConfig{}
		}
		file.Runtime.Timeout = v
	}
}

func applyViperToProvider(s *agent.SearchConfig, provider string) {
	if s == nil {
		return
	}
	baseURLKey := fmt.Sprintf("search.providers.%s.base_url", provider)
	apiKeyKey := fmt.Sprintf("search.providers.%s.api_key", provider)
	hasBaseURL := viper.IsSet(baseURLKey)
	hasAPIKey := viper.IsSet(apiKeyKey)
	if !hasBaseURL && !hasAPIKey {
		return
	}
	if s.Providers == nil {
		s.Providers = make(map[string]agent.SearchProviderConfig)
	}
	pc := s.Providers[provider]
	if hasBaseURL {
		pc.BaseURL = viper.GetString(baseURLKey)
	}
	if hasAPIKey {
		pc.APIKey = viper.GetString(apiKeyKey)
	}
	s.Providers[provider] = pc
}

func applyViperToRuntime(cmd *cobra.Command, rt *agent.FileRuntime) {
	if rt == nil {
		return
	}
	// Prefer explicit flag; otherwise env (avoid bool flag default forcing false).
	if cmd.Flags().Changed("log-level") {
		if v := strings.TrimSpace(viper.GetString("log-level")); v != "" {
			rt.LogLevel = v
		}
	} else if _, ok := lookupEnv("MASTER_LOG_LEVEL"); ok {
		if v := strings.TrimSpace(viper.GetString("log-level")); v != "" {
			rt.LogLevel = v
		}
	}
	if cmd.Flags().Changed("verbose") {
		rt.Verbose = viper.GetBool("verbose")
	} else if _, ok := lookupEnv("MASTER_VERBOSE"); ok {
		rt.Verbose = viper.GetBool("verbose")
	}
}

func lookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
