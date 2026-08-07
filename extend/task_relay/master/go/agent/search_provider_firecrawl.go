package agent

import (
	"context"
	"fmt"
	"net/http"
)

// firecrawlProvider talks to the Firecrawl v2 REST API.
type firecrawlProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	apiKey  string
	http    httpDoer
}

func newFirecrawlProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "firecrawl")
	if !providerEnabled("firecrawl", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.firecrawl.dev"
	}
	timeout := providerTimeout(cfg, pc)
	return &firecrawlProvider{
		cfg:     pc,
		baseURL: baseURL,
		apiKey:  pc.APIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *firecrawlProvider) Name() string          { return "firecrawl" }
func (p *firecrawlProvider) IsAvailable() bool     { return p.baseURL != "" && p.apiKey != "" }
func (p *firecrawlProvider) SupportsSearch() bool  { return true }
func (p *firecrawlProvider) SupportsExtract() bool { return true }

func (p *firecrawlProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("firecrawl provider is nil")
	}
	body := map[string]any{
		"query": query,
		"limit": limit,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	data, err := doPostJSON(ctx, p.http, p.baseURL+"/v2/search", body, headers)
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	inner, ok := data["data"].(map[string]any)
	if !ok {
		return &SearchResponse{Success: false, Error: "firecrawl response missing data"}, nil
	}
	raw, ok := inner["web"].([]any)
	if !ok {
		return &SearchResponse{Success: false, Error: "firecrawl response missing data.web"}, nil
	}

	out := make([]SearchResult, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, SearchResult{
			Title:       stringOf(m["title"]),
			URL:         stringOf(m["url"]),
			Description: stringOf(m["description"]),
			Position:    i + 1,
		})
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func (p *firecrawlProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("firecrawl provider is nil")
	}
	if len(urls) == 0 {
		return &ExtractResponse{Success: false, Error: "urls is required"}, nil
	}
	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	out := make([]ExtractResult, 0, len(urls))
	for _, u := range urls {
		body := map[string]any{
			"url":     u,
			"formats": []string{"markdown"},
		}
		data, err := doPostJSON(ctx, p.http, p.baseURL+"/v2/scrape", body, headers)
		if err != nil {
			out = append(out, ExtractResult{URL: u, Error: err.Error()})
			continue
		}
		inner, ok := data["data"].(map[string]any)
		if !ok {
			out = append(out, ExtractResult{URL: u, Error: "firecrawl response missing data"})
			continue
		}
		title := ""
		if meta, ok := inner["metadata"].(map[string]any); ok {
			title = stringOf(meta["title"])
		}
		out = append(out, ExtractResult{
			URL:     u,
			Title:   title,
			Content: stringOf(inner["markdown"]),
		})
	}
	return &ExtractResponse{Success: true, Results: out}, nil
}
