package search

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeLegacyMapping(t *testing.T) {
	cfg := &Config{
		Provider: "tavily",
		BaseURL:  "http://gw:8000",
		APIKey:   "k",
	}
	Normalize(cfg)
	require.Contains(t, cfg.Providers, "tavily")
	assert.Equal(t, "http://gw:8000", cfg.Providers["tavily"].BaseURL)
	assert.Equal(t, "k", cfg.Providers["tavily"].APIKey)
	assert.Equal(t, 5, cfg.MaxResults)
	assert.Equal(t, 30, cfg.TimeoutSeconds)
}

func TestNormalizeLegacyPerplexity(t *testing.T) {
	cfg := &Config{Provider: "perplexity", BaseURL: "http://p", APIKey: "k"}
	Normalize(cfg)
	require.Contains(t, cfg.Providers, "perplexity")
	assert.True(t, *cfg.Providers["perplexity"].IsBearer)
}

func TestNormalizeLegacyUnknownMapsToGateway(t *testing.T) {
	cfg := &Config{Provider: "my-gateway", BaseURL: "http://g", APIKey: "k"}
	Normalize(cfg)
	require.Contains(t, cfg.Providers, "gateway")
	assert.Equal(t, "http://g", cfg.Providers["gateway"].BaseURL)
}

func TestNormalizeTrimsAndDefaults(t *testing.T) {
	cfg := &Config{
		SearchBackend: "  Tavily ",
		Providers: map[string]ProviderConfig{
			"EXA": {BaseURL: "https://api.exa.ai/", APIKey: " k "},
		},
	}
	Normalize(cfg)
	assert.Equal(t, "tavily", cfg.SearchBackend)
	require.Contains(t, cfg.Providers, "exa")
	assert.Equal(t, "https://api.exa.ai", cfg.Providers["exa"].BaseURL)
	assert.Equal(t, "k", cfg.Providers["exa"].APIKey)
}

func TestIsEnabled(t *testing.T) {
	assert.False(t, (*Config)(nil).IsEnabled())
	assert.False(t, (&Config{}).IsEnabled())
	assert.True(t, (&Config{Enabled: BoolPtr(true)}).IsEnabled())
	assert.False(t, (&Config{Enabled: BoolPtr(false)}).IsEnabled())
	assert.True(t, (&Config{Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t", APIKey: "k"},
	}}).IsEnabled())
	assert.False(t, (&Config{Providers: map[string]ProviderConfig{
		"tavily": {Enabled: BoolPtr(false), BaseURL: "http://t", APIKey: "k"},
	}}).IsEnabled())
}

func TestValidate(t *testing.T) {
	// Unknown backend name.
	cfg := &Config{SearchBackend: "nope", Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t", APIKey: "k"},
	}}
	require.Error(t, Validate(cfg))

	// Missing api_key for a keyed provider.
	cfg = &Config{Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t"},
	}}
	require.Error(t, Validate(cfg))

	// searxng needs no key.
	cfg = &Config{Providers: map[string]ProviderConfig{
		"searxng": {BaseURL: "http://localhost:8080"},
	}}
	require.NoError(t, Validate(cfg))

	// Unresolved env ref.
	cfg = &Config{Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t", APIKey: "${TAVILY_API_KEY}"},
	}}
	require.Error(t, Validate(cfg))

	// Disabled config validates clean.
	cfg = &Config{Enabled: BoolPtr(false), Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t"},
	}}
	require.NoError(t, Validate(cfg))
}

func TestProviderConfigForInheritsTimeout(t *testing.T) {
	cfg := &Config{TimeoutSeconds: 10, Providers: map[string]ProviderConfig{
		"tavily": {BaseURL: "http://t"},
	}}
	pc := ProviderConfigFor(cfg, "tavily")
	assert.Equal(t, 10, pc.TimeoutSeconds)
	assert.Equal(t, 10, ProviderConfigFor(cfg, "missing").TimeoutSeconds)
}

func TestProviderTimeout(t *testing.T) {
	assert.Equal(t, 30*time.Second, ProviderTimeout(nil, ProviderConfig{}))
	assert.Equal(t, 5*time.Second, ProviderTimeout(&Config{TimeoutSeconds: 5}, ProviderConfig{}))
	assert.Equal(t, 7*time.Second, ProviderTimeout(&Config{TimeoutSeconds: 5}, ProviderConfig{TimeoutSeconds: 7}))
}

func TestRequiresAPIKey(t *testing.T) {
	assert.False(t, RequiresAPIKey("searxng"))
	assert.False(t, RequiresAPIKey("ddgs"))
	assert.True(t, RequiresAPIKey("tavily"))
	assert.True(t, RequiresAPIKey("firecrawl"))
}
