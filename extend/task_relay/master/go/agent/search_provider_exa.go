package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// exaProvider talks to the Exa REST API.
type exaProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	apiKey  string
	http    httpDoer
}

func newExaProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "exa")
	if !providerEnabled("exa", pc) {
		return nil
	}
	if pc.BaseURL == "" && pc.APIKey == "" {
		return nil
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.exa.ai"
	}
	timeout := providerTimeout(cfg, pc)
	return &exaProvider{
		cfg:     pc,
		baseURL: baseURL,
		apiKey:  pc.APIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *exaProvider) Name() string          { return "exa" }
func (p *exaProvider) IsAvailable() bool     { return p.baseURL != "" && p.apiKey != "" }
func (p *exaProvider) SupportsSearch() bool  { return true }
func (p *exaProvider) SupportsExtract() bool { return true }

func (p *exaProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("exa provider is nil")
	}
	body := map[string]any{
		"query":      query,
		"numResults": limit,
		"contents": map[string]any{
			"highlights": true,
		},
	}
	headers := map[string]string{
		"x-api-key":         p.apiKey,
		"x-exa-integration": "xhermes-agent",
	}
	data, err := doPostJSON(ctx, p.http, p.baseURL+"/search", body, headers)
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	raw, ok := data["results"].([]any)
	if !ok {
		return &SearchResponse{Success: false, Error: "exa response missing results"}, nil
	}

	out := make([]SearchResult, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		desc := ""
		if hi, ok := m["highlights"].([]any); ok {
			parts := make([]string, 0, len(hi))
			for _, h := range hi {
				parts = append(parts, stringOf(h))
			}
			desc = strings.Join(parts, " ")
		}
		// Tolerate older object-shaped highlights.
		if desc == "" && len(raw) > 0 {
			highlightsRaw, _ := json.Marshal(m["highlights"])
			_ = highlightsRaw
		}
		out = append(out, SearchResult{
			Title:       stringOf(m["title"]),
			URL:         stringOf(m["url"]),
			Description: desc,
			Position:    i + 1,
		})
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func (p *exaProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("exa provider is nil")
	}
	if len(urls) == 0 {
		return &ExtractResponse{Success: false, Error: "urls is required"}, nil
	}
	body := map[string]any{
		"urls": urls,
		"text": true,
	}
	headers := map[string]string{
		"x-api-key":         p.apiKey,
		"x-exa-integration": "xhermes-agent",
	}
	data, err := doPostJSON(ctx, p.http, p.baseURL+"/contents", body, headers)
	if err != nil {
		return &ExtractResponse{Success: false, Error: err.Error()}, nil
	}

	raw, ok := data["results"].([]any)
	if !ok {
		return &ExtractResponse{Success: false, Error: "exa response missing results"}, nil
	}

	out := make([]ExtractResult, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, ExtractResult{
			URL:     stringOf(m["url"]),
			Title:   stringOf(m["title"]),
			Content: stringOf(m["text"]),
		})
	}
	return &ExtractResponse{Success: true, Results: out}, nil
}
