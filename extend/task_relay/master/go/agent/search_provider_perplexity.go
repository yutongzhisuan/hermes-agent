package agent

import (
	"context"
	"fmt"
)

// perplexityProvider uses the same Tavily-shaped endpoints as Tavily but sends
// the API key as an Authorization Bearer header.
type perplexityProvider struct {
	cfg    SearchProviderConfig
	client *tavilyClient
}

func newPerplexityProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "perplexity")
	if !providerEnabled("perplexity", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	return &perplexityProvider{
		cfg:    pc,
		client: newTavilyClient(pc.BaseURL, pc.APIKey, true, providerTimeout(cfg, pc)),
	}
}

func (p *perplexityProvider) Name() string          { return "perplexity" }
func (p *perplexityProvider) IsAvailable() bool     { return p.cfg.BaseURL != "" && p.cfg.APIKey != "" }
func (p *perplexityProvider) SupportsSearch() bool  { return true }
func (p *perplexityProvider) SupportsExtract() bool { return true }

func (p *perplexityProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("perplexity provider is nil")
	}
	return p.client.Search(ctx, query, limit, "", "", "")
}

func (p *perplexityProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("perplexity provider is nil")
	}
	return p.client.Extract(ctx, urls, "", "")
}
