package agent

import (
	"context"
	"fmt"
	"net/http"
)

const parallelBetaHeader = "search-extract-2025-10-10"

// parallelProvider talks to the Parallel REST API (v1beta protocol).
type parallelProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	apiKey  string
	http    httpDoer
}

func newParallelProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "parallel")
	if !providerEnabled("parallel", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.parallel.ai"
	}
	timeout := providerTimeout(cfg, pc)
	return &parallelProvider{
		cfg:     pc,
		baseURL: baseURL,
		apiKey:  pc.APIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *parallelProvider) Name() string          { return "parallel" }
func (p *parallelProvider) IsAvailable() bool     { return p.baseURL != "" && p.apiKey != "" }
func (p *parallelProvider) SupportsSearch() bool  { return true }
func (p *parallelProvider) SupportsExtract() bool { return true }

func (p *parallelProvider) headers() map[string]string {
	return map[string]string{
		"x-api-key":      p.apiKey,
		"parallel-beta":  parallelBetaHeader,
		"Content-Type":   "application/json",
		"Accept-version": "v1beta",
	}
}

func (p *parallelProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("parallel provider is nil")
	}
	body := map[string]any{
		"search_queries": []string{query},
		"mode":           "one-shot",
		"max_results":    limit,
	}
	data, err := doPostJSON(ctx, p.http, p.baseURL+"/v1beta/search", body, p.headers())
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	raw, ok := data["search_results"].([]any)
	if !ok {
		return &SearchResponse{Success: false, Error: "parallel response missing search_results"}, nil
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
			Description: stringOf(firstNonEmpty(m, "snippet", "summary", "description")),
			Position:    i + 1,
		})
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func (p *parallelProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("parallel provider is nil")
	}
	if len(urls) == 0 {
		return &ExtractResponse{Success: false, Error: "urls is required"}, nil
	}
	body := map[string]any{
		"urls":         urls,
		"full_content": true,
	}
	data, err := doPostJSON(ctx, p.http, p.baseURL+"/v1beta/extract", body, p.headers())
	if err != nil {
		return &ExtractResponse{Success: false, Error: err.Error()}, nil
	}

	out := make([]ExtractResult, 0, len(urls))
	if raw, ok := data["results"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, ExtractResult{
				URL:     stringOf(m["url"]),
				Title:   stringOf(m["title"]),
				Content: stringOf(firstNonEmpty(m, "content", "markdown", "text")),
			})
		}
	}
	// Parallel reports per-URL failures inside errors[] rather than as HTTP errors.
	if raw, ok := data["errors"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, ExtractResult{
				URL:   stringOf(m["url"]),
				Error: stringOf(firstNonEmpty(m, "error", "message")),
			})
		}
	}
	return &ExtractResponse{Success: true, Results: out}, nil
}

// firstNonEmpty returns the first non-empty string value among the keys.
func firstNonEmpty(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := stringOf(m[k]); v != "" {
			return v
		}
	}
	return ""
}
