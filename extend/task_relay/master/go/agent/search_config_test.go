package agent

import (
	"testing"
)

func TestNormalizeSearchConfigLegacyMapping(t *testing.T) {
	cfg := &SearchConfig{
		Enabled:    new(true),
		BaseURL:    "http://old.example.com",
		APIKey:     "legacy-key",
		Provider:   "tavily",
		MaxResults: 10,
	}
	normalizeSearchConfig(cfg)

	if cfg.Provider != "tavily" {
		t.Errorf("Provider preserved, got %q", cfg.Provider)
	}
	if cfg.Providers == nil {
		t.Fatal("Providers map not created")
	}
	pc, ok := cfg.Providers["tavily"]
	if !ok {
		t.Fatal("legacy fields not mapped to providers.tavily")
	}
	if pc.BaseURL != "http://old.example.com" {
		t.Errorf("base_url mapping, got %q", pc.BaseURL)
	}
	if pc.APIKey != "legacy-key" {
		t.Errorf("api_key mapping, got %q", pc.APIKey)
	}
}

func TestNormalizeSearchConfigLegacyUnknownProvider(t *testing.T) {
	cfg := &SearchConfig{
		Enabled:  new(true),
		BaseURL:  "http://gateway.example.com",
		APIKey:   "gateway-key",
		Provider: "my-gateway",
	}
	normalizeSearchConfig(cfg)
	pc, ok := cfg.Providers["gateway"]
	if !ok {
		t.Fatal("unknown legacy provider should map to gateway provider")
	}
	if pc.BaseURL != "http://gateway.example.com" {
		t.Errorf("gateway base_url, got %q", pc.BaseURL)
	}
}

func TestNormalizeSearchConfigNoOverride(t *testing.T) {
	cfg := &SearchConfig{
		Enabled:  new(true),
		BaseURL:  "http://legacy.com",
		APIKey:   "legacy",
		Provider: "tavily",
		Providers: map[string]SearchProviderConfig{
			"tavily": {BaseURL: "http://explicit.com", APIKey: "explicit"},
		},
	}
	normalizeSearchConfig(cfg)
	pc := cfg.Providers["tavily"]
	if pc.BaseURL != "http://explicit.com" {
		t.Errorf("explicit providers should not be overwritten by legacy fields, got %q", pc.BaseURL)
	}
}

func TestNormalizeSearchConfigPerplexityLegacy(t *testing.T) {
	cfg := &SearchConfig{
		Enabled:  new(true),
		Provider: "perplexity",
		BaseURL:  "http://p.com",
		APIKey:   "pk",
	}
	normalizeSearchConfig(cfg)
	pc := cfg.Providers["perplexity"]
	if pc.IsBearer == nil || !*pc.IsBearer {
		t.Error("perplexity legacy should set IsBearer=true")
	}
}

func TestSearchConfigIsEnabled(t *testing.T) {
	if (&SearchConfig{}).IsEnabled() {
		t.Error("empty config should not be enabled")
	}
	if !(&SearchConfig{Enabled: new(true)}).IsEnabled() {
		t.Error("Enabled=true should be enabled")
	}
	if (&SearchConfig{Enabled: new(false)}).IsEnabled() {
		t.Error("Enabled=false should not be enabled")
	}
	if !(&SearchConfig{Providers: map[string]SearchProviderConfig{"searxng": {BaseURL: "http://s"}}}).IsEnabled() {
		t.Error("provider with base_url should be enabled")
	}
}

func TestValidateSearchConfig(t *testing.T) {
	cfg := &SearchConfig{
		Enabled:        new(true),
		MaxResults:     5,
		TimeoutSeconds: 30,
		Providers: map[string]SearchProviderConfig{
			"tavily": {BaseURL: "http://t.com", APIKey: "k"},
		},
	}
	if err := validateSearchConfig(cfg); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestNormalizeSearchConfigDefaults(t *testing.T) {
	cfg := &SearchConfig{
		Enabled: new(true),
		Providers: map[string]SearchProviderConfig{
			"tavily": {BaseURL: "http://t.com", APIKey: "k"},
		},
	}
	normalizeSearchConfig(cfg)
	if cfg.MaxResults != 5 {
		t.Errorf("expected default max_results 5, got %d", cfg.MaxResults)
	}
	if cfg.TimeoutSeconds != 30 {
		t.Errorf("expected default timeout 30, got %d", cfg.TimeoutSeconds)
	}
	pc := cfg.Providers["tavily"]
	if pc.TimeoutSeconds != 30 {
		t.Errorf("expected inherited timeout 30, got %d", pc.TimeoutSeconds)
	}
}

func TestProviderConfig(t *testing.T) {
	cfg := &SearchConfig{
		TimeoutSeconds: 10,
		Providers: map[string]SearchProviderConfig{
			"tavily": {BaseURL: "http://t.com"},
		},
	}
	pc := providerConfig(cfg, "tavily")
	if pc.BaseURL != "http://t.com" {
		t.Errorf("got %q", pc.BaseURL)
	}
	if pc.TimeoutSeconds != 10 {
		t.Errorf("expected timeout to inherit from global, got %d", pc.TimeoutSeconds)
	}
	pc2 := providerConfig(cfg, "exa")
	if pc2.BaseURL != "" {
		t.Errorf("missing provider should return empty, got %q", pc2.BaseURL)
	}
}
