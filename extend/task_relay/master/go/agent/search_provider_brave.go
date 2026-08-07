package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// braveProvider uses the Brave Search free tier Data-for-Search API.
type braveProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	apiKey  string
	http    httpDoer
}

func newBraveProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "brave-free")
	if !providerEnabled("brave-free", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.search.brave.com"
	}
	timeout := providerTimeout(cfg, pc)
	return &braveProvider{
		cfg:     pc,
		baseURL: baseURL,
		apiKey:  pc.APIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *braveProvider) Name() string          { return "brave-free" }
func (p *braveProvider) IsAvailable() bool     { return p.apiKey != "" }
func (p *braveProvider) SupportsSearch() bool  { return true }
func (p *braveProvider) SupportsExtract() bool { return false }

func (p *braveProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("brave-free provider is nil")
	}
	if p.apiKey == "" {
		return &SearchResponse{Success: false, Error: "brave-free api_key is not set"}, nil
	}
	// Brave caps count at 20.
	count := limit
	if count <= 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}

	u := p.baseURL + "/res/v1/web/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	req.URL.RawQuery = q.Encode()

	headers := map[string]string{
		"X-Subscription-Token": p.apiKey,
		"Accept":               "application/json",
	}
	raw, err := doGet(ctx, p.http, req.URL.String(), headers)
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return &SearchResponse{Success: false, Error: "could not parse brave response"}, nil
	}

	out := make([]SearchResult, 0, len(payload.Web.Results))
	for i, r := range payload.Web.Results {
		out = append(out, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Description,
			Position:    i + 1,
		})
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func (p *braveProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	return &ExtractResponse{Success: false, Error: "brave-free does not support extract"}, nil
}
