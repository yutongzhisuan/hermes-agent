package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/task_relay/master/agent/search"
)

// exaProvider calls the Exa REST API (api.exa.ai).
type exaProvider struct {
	search.Base
}

func newExa(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("exa", cfg, search.BaseOpts{
		DefaultBaseURL: "https://api.exa.ai",
		Search:         true,
		Extract:        true,
	})
	if !ok {
		return nil
	}
	return &exaProvider{Base: *base}
}

func (p *exaProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	var out struct {
		Results []struct {
			URL        string   `json:"url"`
			Title      string   `json:"title"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("x-api-key", p.APIKey).
		SetBody(map[string]any{
			"query":      query,
			"numResults": limit,
			"contents":   map[string]any{"highlights": true},
		}).
		Post(p.BaseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("exa search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("exa search: decode response: %w", err)
	}

	results := make([]search.SearchResult, len(out.Results))
	for i, r := range out.Results {
		desc := ""
		if len(r.Highlights) > 0 {
			desc = r.Highlights[0]
		}
		results[i] = search.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: desc,
			Position:    i + 1,
		}
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

func (p *exaProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	var out struct {
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"results"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("x-api-key", p.APIKey).
		SetBody(map[string]any{"urls": urls, "text": true}).
		Post(p.BaseURL + "/contents")
	if err != nil {
		return nil, fmt.Errorf("exa extract: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("exa extract: decode response: %w", err)
	}

	results := make([]search.ExtractResult, len(out.Results))
	for i, r := range out.Results {
		results[i] = search.ExtractResult{URL: r.URL, Title: r.Title, Content: r.Text}
	}
	return &search.ExtractResponse{Success: true, Results: results}, nil
}
