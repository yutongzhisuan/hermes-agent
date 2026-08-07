package search

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	CapabilitySearch  = "search"
	CapabilityExtract = "extract"
)

// KnownProviders lists every provider the Go master understands.
var KnownProviders = []string{
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

// LegacyPreference matches the Python side in web_search_registry.py.
var LegacyPreference = []string{
	"firecrawl",
	"parallel",
	"tavily",
	"exa",
	"searxng",
	"brave-free",
	"ddgs",
}

// ProviderConfig holds provider-specific settings.
// Every provider has the same shape; unused provider-specific fields are ignored.
type ProviderConfig struct {
	Enabled        *bool  `json:"enabled" yaml:"enabled"`
	BaseURL        string `json:"base_url" yaml:"base_url"`
	APIKey         string `json:"api_key" yaml:"api_key"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`

	// Provider-specific options.
	SearchDepth string `json:"search_depth" yaml:"search_depth"` // tavily/gateway only
	IsBearer    *bool  `json:"is_bearer" yaml:"is_bearer"`       // gateway only: true for Perplexity-shaped gateway
}

// Config configures web_search / web_extract backends.
type Config struct {
	// Per-capability explicit selection.
	SearchBackend  string `json:"search_backend" yaml:"search_backend"`
	ExtractBackend string `json:"extract_backend" yaml:"extract_backend"`
	// Shared fallback when per-capability fields are empty.
	Backend string `json:"backend" yaml:"backend"`

	MaxResults     int `json:"max_results" yaml:"max_results"`
	TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`

	Providers map[string]ProviderConfig `json:"providers" yaml:"providers"`

	// Global on/off switch. nil means infer from presence of providers.
	Enabled *bool `json:"enabled" yaml:"enabled"`

	// Legacy single-provider fields. Normalize maps these into Providers for
	// backward compatibility with existing master.example.yaml.
	Provider string `json:"provider" yaml:"provider"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
}

// Normalize applies defaults and legacy field mapping in place.
func Normalize(s *Config) {
	if s == nil {
		return
	}

	// Legacy field mapping: if the user provided the old top-level
	// provider/base_url/api_key and no nested providers map, materialize the
	// matching provider block. This keeps existing deployments that point at a
	// Tavily-compatible gateway working unchanged.
	if len(s.Providers) == 0 && (strings.TrimSpace(s.BaseURL) != "" || strings.TrimSpace(s.APIKey) != "" || strings.TrimSpace(s.Provider) != "") {
		if s.Providers == nil {
			s.Providers = make(map[string]ProviderConfig)
		}
		legacy := ProviderConfig{
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
	normalized := make(map[string]ProviderConfig, len(s.Providers))
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
func (s *Config) IsEnabled() bool {
	if s == nil {
		return false
	}
	if s.Enabled != nil {
		return *s.Enabled
	}
	// When not explicitly disabled/enabled, infer from provider presence.
	for name, pc := range s.Providers {
		if !ProviderEnabled(name, pc) {
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

// Validate checks the normalized config for unknown backends and incomplete providers.
func Validate(s *Config) error {
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
		if !IsKnownProvider(strings.ToLower(strings.TrimSpace(path))) {
			return fmt.Errorf("unknown search backend %q (known: %s)", path, strings.Join(KnownProviders, ", "))
		}
	}

	for name, pc := range s.Providers {
		if !ProviderEnabled(name, pc) {
			continue
		}
		if strings.Contains(pc.BaseURL, "${") || strings.Contains(pc.APIKey, "${") {
			return fmt.Errorf("search.providers.%s contains unresolved ${ENV} reference", name)
		}
		if pc.BaseURL == "" && RequiresBaseURL(name) {
			return fmt.Errorf("search.providers.%s requires base_url", name)
		}
		if pc.APIKey == "" && RequiresAPIKey(name) {
			return fmt.Errorf("search.providers.%s requires api_key", name)
		}
	}

	return nil
}

// ProviderConfigFor returns the provider-specific config with global defaults applied.
func ProviderConfigFor(s *Config, name string) ProviderConfig {
	if s == nil {
		return ProviderConfig{TimeoutSeconds: 30}
	}
	pc, ok := s.Providers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return ProviderConfig{TimeoutSeconds: s.TimeoutSeconds}
	}
	if pc.TimeoutSeconds <= 0 {
		pc.TimeoutSeconds = s.TimeoutSeconds
	}
	return pc
}

// ProviderTimeout resolves the effective timeout for a provider config.
func ProviderTimeout(cfg *Config, pc ProviderConfig) time.Duration {
	d := pc.TimeoutSeconds
	if d <= 0 && cfg != nil {
		d = cfg.TimeoutSeconds
	}
	if d <= 0 {
		d = 30
	}
	return time.Duration(d) * time.Second
}

// ProviderEnabled reports whether a provider block is enabled.
func ProviderEnabled(name string, pc ProviderConfig) bool {
	if pc.Enabled != nil {
		return *pc.Enabled
	}
	return true
}

// IsKnownProvider reports whether name is a supported provider.
func IsKnownProvider(name string) bool {
	return slices.Contains(KnownProviders, name)
}

// RequiresBaseURL reports whether a provider needs an explicit base URL.
func RequiresBaseURL(name string) bool {
	// All current providers require a base URL. Gateway may use an empty base
	// only in test scenarios; still require it explicitly.
	return true
}

// RequiresAPIKey reports whether a provider needs an API key.
func RequiresAPIKey(name string) bool {
	switch name {
	case "searxng", "ddgs":
		return false
	default:
		return true
	}
}

// BoolPtr returns a pointer to v.
func BoolPtr(v bool) *bool {
	return new(v)
}
