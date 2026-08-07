package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/task_relay/master/agent/search"
)

// parallelBetaHeader is mandatory for the Parallel search/extract beta API;
// omitting it returns 404.
const parallelBetaHeader = "search-extract-2025-10-10"

// parallelProvider calls the Parallel v1beta REST API (SDK 0.4.2 protocol).
type parallelProvider struct {
	search.Base
}

func newParallel(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("parallel", cfg, search.BaseOpts{
		DefaultBaseURL: "https://api.parallel.ai",
		Search:         true,
		Extract:        true,
	})
	if !ok {
		return nil
	}
	return &parallelProvider{Base: *base}
}

func (p *parallelProvider) auth() *parallelProvider {
	return p
}

// Parallel returns search results shaped like {"search_results": [...]} or
// {"results": [...]}; parse both tolerantly.
func (p *parallelProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	body := map[string]any{
		"search_queries": []string{query},
		"objective":      query,
		"mode":           "fast",
		"max_results":    limit,
	}

	var out struct {
		SearchResults []jsonMap `json:"search_results"`
		Results       []jsonMap `json:"results"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("x-api-key", p.APIKey).
		SetHeader("parallel-beta", parallelBetaHeader).
		SetBody(body).
		Post(p.BaseURL + "/v1beta/search")
	if err != nil {
		return nil, fmt.Errorf("parallel search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("parallel search: decode response: %w", err)
	}

	raw := out.SearchResults
	if len(raw) == 0 {
		raw = out.Results
	}
	results := make([]search.SearchResult, 0, len(raw))
	for i, m := range raw {
		results = append(results, search.SearchResult{
			Title:       firstNonEmpty(m["title"], m["name"]),
			URL:         firstNonEmpty(m["url"], m["link"]),
			Description: firstNonEmpty(m["snippet"], m["description"], m["content"]),
			Position:    i + 1,
		})
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

// Extract uses the beta extract API. Per-URL failures arrive in the response
// errors[] array instead of an HTTP error.
func (p *parallelProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	var out struct {
		Results []jsonMap `json:"results"`
		Errors  []jsonMap `json:"errors"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("x-api-key", p.APIKey).
		SetHeader("parallel-beta", parallelBetaHeader).
		SetBody(map[string]any{"urls": urls, "full_content": true}).
		Post(p.BaseURL + "/v1beta/extract")
	if err != nil {
		return nil, fmt.Errorf("parallel extract: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("parallel extract: decode response: %w", err)
	}

	results := make([]search.ExtractResult, 0, len(out.Results)+len(out.Errors))
	for _, m := range out.Results {
		results = append(results, search.ExtractResult{
			URL:     firstNonEmpty(m["url"], m["source_url"]),
			Title:   firstNonEmpty(m["title"], m["source_title"]),
			Content: firstNonEmpty(m["content"], m["text"]),
		})
	}
	for _, m := range out.Errors {
		results = append(results, search.ExtractResult{
			URL:   firstNonEmpty(m["url"], m["source_url"]),
			Error: firstNonEmpty(m["error"], m["message"]),
		})
	}
	return &search.ExtractResponse{Success: true, Results: results}, nil
}

// jsonMap is a tolerant JSON object accessor.
type jsonMap = map[string]any

func firstNonEmpty(vals ...any) string {
	for _, v := range vals {
		if s := search.StringOf(v); s != "" {
			return s
		}
	}
	return ""
}
