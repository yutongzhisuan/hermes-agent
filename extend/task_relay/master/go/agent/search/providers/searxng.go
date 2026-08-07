package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/infa/task_relay/master/agent/search"
)

// searxngProvider talks to a user-hosted SearXNG instance (search only).
type searxngProvider struct {
	search.Base
}

func newSearxng(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("searxng", cfg, search.BaseOpts{Search: true})
	if !ok {
		return nil
	}
	return &searxngProvider{Base: *base}
}

func (p *searxngProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	var out struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{"q": query, "format": "json", "pageno": "1"}).
		Get(p.BaseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("searxng search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("searxng search: decode response: %w", err)
	}

	results := out.Results
	// SearXNG returns results in instance-internal order; sort by score desc to
	// match the Python provider behavior.
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	normalized := make([]search.SearchResult, len(results))
	for i, r := range results {
		normalized[i] = search.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    i + 1,
		}
	}
	return &search.SearchResponse{Success: true, Results: normalized}, nil
}

func (p *searxngProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	return nil, fmt.Errorf("searxng does not support extraction")
}
