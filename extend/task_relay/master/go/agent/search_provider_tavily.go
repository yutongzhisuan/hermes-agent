package agent

import (
	"context"
	"fmt"
)

// tavilyProvider uses the Tavily-shaped HTTP API.
type tavilyProvider struct {
	cfg    SearchProviderConfig
	client *tavilyClient
}

func newTavilyProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "tavily")
	if !providerEnabled("tavily", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	return &tavilyProvider{
		cfg:    pc,
		client: newTavilyClient(pc.BaseURL, pc.APIKey, false, providerTimeout(cfg, pc)),
	}
}

func (p *tavilyProvider) Name() string          { return "tavily" }
func (p *tavilyProvider) IsAvailable() bool     { return p.cfg.BaseURL != "" && p.cfg.APIKey != "" }
func (p *tavilyProvider) SupportsSearch() bool  { return true }
func (p *tavilyProvider) SupportsExtract() bool { return true }

func (p *tavilyProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("tavily provider is nil")
	}
	return p.client.Search(ctx, query, limit, p.cfg.SearchDepth, "", "")
}

func (p *tavilyProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("tavily provider is nil")
	}
	return p.client.Extract(ctx, urls, "", "")
}
