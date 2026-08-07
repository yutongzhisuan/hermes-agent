package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// searxngProvider talks to a user-hosted SearXNG instance.
type searxngProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	http    httpDoer
}

func newSearxngProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "searxng")
	if !providerEnabled("searxng", pc) {
		return nil
	}
	if pc.BaseURL == "" {
		return nil
	}
	timeout := providerTimeout(cfg, pc)
	return &searxngProvider{
		cfg:     pc,
		baseURL: pc.BaseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *searxngProvider) Name() string          { return "searxng" }
func (p *searxngProvider) IsAvailable() bool     { return p.baseURL != "" }
func (p *searxngProvider) SupportsSearch() bool  { return true }
func (p *searxngProvider) SupportsExtract() bool { return false }

func (p *searxngProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("searxng provider is nil")
	}
	if p.baseURL == "" {
		return &SearchResponse{Success: false, Error: "searxng base_url is not set"}, nil
	}

	u := p.baseURL + "/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("pageno", "1")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	raw, err := doGet(ctx, p.http, req.URL.String(), map[string]string{"Accept": "application/json"})
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	var payload struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return &SearchResponse{Success: false, Error: "could not parse searxng response"}, nil
	}

	// Sort by score descending and cap to limit.
	sort.Slice(payload.Results, func(i, j int) bool {
		return payload.Results[i].Score > payload.Results[j].Score
	})
	if limit > 0 && limit < len(payload.Results) {
		payload.Results = payload.Results[:limit]
	}

	out := make([]SearchResult, 0, len(payload.Results))
	for i, r := range payload.Results {
		out = append(out, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    i + 1,
		})
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func (p *searxngProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	return &ExtractResponse{Success: false, Error: "searxng does not support extract"}, nil
}
