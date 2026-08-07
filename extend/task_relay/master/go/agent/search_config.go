package agent

import (
	"fmt"
	"strings"
)

const (
	searchProviderTavily     = "tavily"
	searchProviderPerplexity = "perplexity"
)

// SearchConfig configures web_search / web_extract HTTP backends.
type SearchConfig struct {
	// Provider is tavily (default) or perplexity.
	Provider string `json:"provider" yaml:"provider"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
	// MaxResults is the default max_results for web_search (1-20).
	MaxResults int `json:"max_results" yaml:"max_results"`
	// TimeoutSeconds is the HTTP timeout; default 30.
	TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`
	// SearchDepth is the default Tavily search_depth hint.
	SearchDepth string `json:"search_depth" yaml:"search_depth"`
	// Enabled defaults to true when the search section is present with credentials.
	Enabled *bool `json:"enabled" yaml:"enabled"`
}

func normalizeSearchConfig(s *SearchConfig) {
	if s == nil {
		return
	}
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if s.Provider == "" {
		s.Provider = searchProviderTavily
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.MaxResults <= 0 {
		s.MaxResults = 5
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 30
	}
}

// IsEnabled reports whether search tools should be registered.
func (s *SearchConfig) IsEnabled() bool {
	if s == nil {
		return false
	}
	if s.Enabled != nil && !*s.Enabled {
		return false
	}
	return strings.TrimSpace(s.BaseURL) != "" && strings.TrimSpace(s.APIKey) != "" &&
		!strings.Contains(s.APIKey, "${") && !strings.Contains(s.BaseURL, "${")
}

func validateSearchConfig(s *SearchConfig) error {
	if s == nil {
		return nil
	}
	if s.Enabled != nil && !*s.Enabled {
		return nil
	}
	if strings.TrimSpace(s.BaseURL) == "" && strings.TrimSpace(s.APIKey) == "" {
		return nil
	}
	if strings.Contains(s.BaseURL, "${") || strings.Contains(s.APIKey, "${") {
		return fmt.Errorf("search.base_url/api_key contains unresolved ${ENV}")
	}
	if strings.TrimSpace(s.BaseURL) == "" || strings.TrimSpace(s.APIKey) == "" {
		return fmt.Errorf("search requires both base_url and api_key")
	}
	switch s.Provider {
	case searchProviderTavily, searchProviderPerplexity:
		return nil
	default:
		return fmt.Errorf("unsupported search provider %q (use tavily|perplexity)", s.Provider)
	}
}
