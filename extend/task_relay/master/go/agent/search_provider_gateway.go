package agent

import (
	"context"
	"fmt"
)

// gatewayProvider preserves the legacy self-hosted Tavily-compatible gateway
// behavior. It can be either Tavily-shaped or Perplexity-shaped (Bearer) via
// the is_bearer option.
type gatewayProvider struct {
	cfg    SearchProviderConfig
	client *tavilyClient
}

func newGatewayProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "gateway")
	if !providerEnabled("gateway", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	bearer := false
	if pc.IsBearer != nil {
		bearer = *pc.IsBearer
	}
	return &gatewayProvider{
		cfg:    pc,
		client: newTavilyClient(pc.BaseURL, pc.APIKey, bearer, providerTimeout(cfg, pc)),
	}
}

func (p *gatewayProvider) Name() string          { return "gateway" }
func (p *gatewayProvider) IsAvailable() bool     { return p.cfg.BaseURL != "" && p.cfg.APIKey != "" }
func (p *gatewayProvider) SupportsSearch() bool  { return true }
func (p *gatewayProvider) SupportsExtract() bool { return true }

func (p *gatewayProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("gateway provider is nil")
	}
	return p.client.Search(ctx, query, limit, p.cfg.SearchDepth, "", "")
}

func (p *gatewayProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("gateway provider is nil")
	}
	return p.client.Extract(ctx, urls, "", "")
}
