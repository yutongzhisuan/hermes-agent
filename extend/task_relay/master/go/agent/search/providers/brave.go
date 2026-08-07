package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/task_relay/master/agent/search"
)

// braveProvider uses the Brave Search free tier Data-for-Search API (search only).
type braveProvider struct {
	search.Base
}

func newBrave(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("brave-free", cfg, search.BaseOpts{
		DefaultBaseURL: "https://api.search.brave.com",
		Search:         true,
	})
	if !ok {
		return nil
	}
	return &braveProvider{Base: *base}
}

func (p *braveProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	if limit > 20 {
		limit = 20 // free tier caps count at 20
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("X-Subscription-Token", p.APIKey).
		SetQueryParams(map[string]string{"q": query, "count": fmt.Sprintf("%d", limit)}).
		Get(p.BaseURL + "/res/v1/web/search")
	if err != nil {
		return nil, fmt.Errorf("brave search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("brave search: decode response: %w", err)
	}

	results := make([]search.SearchResult, len(out.Web.Results))
	for i, r := range out.Web.Results {
		results[i] = search.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Description,
			Position:    i + 1,
		}
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

func (p *braveProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	return nil, fmt.Errorf("brave-free does not support extraction")
}
