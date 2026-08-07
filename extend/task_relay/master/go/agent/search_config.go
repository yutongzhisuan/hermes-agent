package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	capabilitySearch  = "search"
	capabilityExtract = "extract"
)

// knownProviderNames lists every provider that the Go master understands.
var knownProviderNames = []string{
	"firecrawl",
	"parallel",
	"tavily",
	"perplexity",
	"gateway",
	"exa",
	"searxng",
	"brave-free",
	"ddgs",
}

// SearchProviderConfig holds provider-specific settings.
// Every provider has the same shape; unused provider-specific fields are ignored.
type SearchProviderConfig struct {
	Enabled        *bool  `json:"enabled" yaml:"enabled"`
	BaseURL        string `json:"base_url" yaml:"base_url"`
	APIKey         string `json:"api_key" yaml:"api_key"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`

	// Provider-specific options.
	SearchDepth string `json:"search_depth" yaml:"search_depth"` // tavily/gateway only
	IsBearer    *bool  `json:"is_bearer" yaml:"is_bearer"`       // gateway only: true for Perplexity-shaped gateway
}

// SearchConfig configures web_search / web_extract backends.
type SearchConfig struct {
	// Per-capability explicit selection.
	SearchBackend  string `json:"search_backend" yaml:"search_backend"`
	ExtractBackend string `json:"extract_backend" yaml:"extract_backend"`
	// Shared fallback when per-capability fields are empty.
	Backend string `json:"backend" yaml:"backend"`

	MaxResults     int `json:"max_results" yaml:"max_results"`
	TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`

	Providers map[string]SearchProviderConfig `json:"providers" yaml:"providers"`

	// Global on/off switch. nil means infer from presence of providers.
	Enabled *bool `json:"enabled" yaml:"enabled"`

	// Legacy single-provider fields. normalizeSearchConfig maps these into
	// Providers for backward compatibility with existing master.example.yaml.
	Provider string `json:"provider" yaml:"provider"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
}

func normalizeSearchConfig(s *SearchConfig) {
	if s == nil {
		return
	}

	// Legacy field mapping: if the user provided the old top-level
	// provider/base_url/api_key and no nested providers map, materialize the
	// matching provider block. This keeps existing deployments that point at a
	// Tavily-compatible gateway working unchanged.
	if len(s.Providers) == 0 && (strings.TrimSpace(s.BaseURL) != "" || strings.TrimSpace(s.APIKey) != "" || strings.TrimSpace(s.Provider) != "") {
		if s.Providers == nil {
			s.Providers = make(map[string]SearchProviderConfig)
		}
		legacy := SearchProviderConfig{
			BaseURL:        strings.TrimSpace(s.BaseURL),
			APIKey:         strings.TrimSpace(s.APIKey),
			TimeoutSeconds: s.TimeoutSeconds,
		}

		provider := strings.ToLower(strings.TrimSpace(s.Provider))
		switch provider {
		case "perplexity":
			legacy.IsBearer = new(true)
			s.Providers["perplexity"] = legacy
		case "tavily", "":
			s.Providers["tavily"] = legacy
		default:
			// Unknown provider name or a self-hosted gateway: preserve it as the
			// generic "gateway" provider. It is Tavily-shaped unless the legacy
			// provider name was "perplexity".
			if provider == "perplexity" {
				legacy.IsBearer = new(true)
			}
			s.Providers["gateway"] = legacy
		}
	}

	s.SearchBackend = strings.ToLower(strings.TrimSpace(s.SearchBackend))
	s.ExtractBackend = strings.ToLower(strings.TrimSpace(s.ExtractBackend))
	s.Backend = strings.ToLower(strings.TrimSpace(s.Backend))

	if s.MaxResults <= 0 {
		s.MaxResults = 5
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 30
	}

	// Normalize provider map keys to lower-case and trim each provider config.
	normalized := make(map[string]SearchProviderConfig, len(s.Providers))
	for name, pc := range s.Providers {
		key := strings.ToLower(strings.TrimSpace(name))
		pc.BaseURL = strings.TrimRight(strings.TrimSpace(pc.BaseURL), "/")
		pc.APIKey = strings.TrimSpace(pc.APIKey)
		pc.SearchDepth = strings.TrimSpace(pc.SearchDepth)
		if pc.TimeoutSeconds <= 0 {
			pc.TimeoutSeconds = s.TimeoutSeconds
		}
		normalized[key] = pc
	}
	s.Providers = normalized
}

// IsEnabled reports whether search tools should be registered.
func (s *SearchConfig) IsEnabled() bool {
	if s == nil {
		return false
	}
	if s.Enabled != nil {
		return *s.Enabled
	}
	// When not explicitly disabled/enabled, infer from provider presence.
	for name, pc := range s.Providers {
		if !providerEnabled(name, pc) {
			continue
		}
		if pc.BaseURL != "" {
			return true
		}
		if pc.APIKey != "" {
			return true
		}
	}
	return false
}

func validateSearchConfig(s *SearchConfig) error {
	if s == nil {
		return nil
	}
	if s.Enabled != nil && !*s.Enabled {
		return nil
	}
	if !s.IsEnabled() {
		return nil
	}

	for _, path := range []string{s.SearchBackend, s.ExtractBackend, s.Backend} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !isKnownProvider(strings.ToLower(strings.TrimSpace(path))) {
			return fmt.Errorf("unknown search backend %q (known: %s)", path, strings.Join(knownProviderNames, ", "))
		}
	}

	for name, pc := range s.Providers {
		if !providerEnabled(name, pc) {
			continue
		}
		if strings.Contains(pc.BaseURL, "${") || strings.Contains(pc.APIKey, "${") {
			return fmt.Errorf("search.providers.%s contains unresolved ${ENV} reference", name)
		}
		if pc.BaseURL == "" && requiresBaseURL(name) {
			return fmt.Errorf("search.providers.%s requires base_url", name)
		}
		if pc.APIKey == "" && requiresAPIKey(name) {
			return fmt.Errorf("search.providers.%s requires api_key", name)
		}
	}

	return nil
}

// providerConfig returns the provider-specific config with global defaults applied.
func providerConfig(s *SearchConfig, name string) SearchProviderConfig {
	if s == nil {
		return SearchProviderConfig{TimeoutSeconds: 30}
	}
	pc, ok := s.Providers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return SearchProviderConfig{TimeoutSeconds: s.TimeoutSeconds}
	}
	if pc.TimeoutSeconds <= 0 {
		pc.TimeoutSeconds = s.TimeoutSeconds
	}
	return pc
}

func providerTimeout(cfg *SearchConfig, pc SearchProviderConfig) time.Duration {
	d := pc.TimeoutSeconds
	if d <= 0 {
		d = cfg.TimeoutSeconds
	}
	if d <= 0 {
		d = 30
	}
	return time.Duration(d) * time.Second
}

func providerEnabled(name string, pc SearchProviderConfig) bool {
	if pc.Enabled != nil {
		return *pc.Enabled
	}
	return true
}

func isKnownProvider(name string) bool {
	return slices.Contains(knownProviderNames, name)
}

func requiresBaseURL(name string) bool {
	// All current providers require a base URL. Gateway may use an empty base
	// only in test scenarios; still require it explicitly.
	return true
}

func requiresAPIKey(name string) bool {
	switch name {
	case "searxng", "ddgs":
		return false
	default:
		return true
	}
}

//go:fix inline
func boolPtr(v bool) *bool {
	return new(v)
}
