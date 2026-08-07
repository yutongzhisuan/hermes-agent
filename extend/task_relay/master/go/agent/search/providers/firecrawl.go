package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/task_relay/master/agent/search"
)

// firecrawlProvider calls the Firecrawl v2 REST API (SDK 4.17.0 default).
type firecrawlProvider struct {
	search.Base
}

func newFirecrawl(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("firecrawl", cfg, search.BaseOpts{
		DefaultBaseURL: "https://api.firecrawl.dev",
		Search:         true,
		Extract:        true,
	})
	if !ok {
		return nil
	}
	return &firecrawlProvider{Base: *base}
}

func (p *firecrawlProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	var out struct {
		Data struct {
			Web []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"web"`
		} `json:"data"`
	}
	resp, err := p.Client.R().
		SetContext(ctx).
		SetAuthToken(p.APIKey).
		SetBody(map[string]any{"query": query, "limit": limit}).
		Post(p.BaseURL + "/v2/search")
	if err != nil {
		return nil, fmt.Errorf("firecrawl search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("firecrawl search: decode response: %w", err)
	}

	results := make([]search.SearchResult, len(out.Data.Web))
	for i, r := range out.Data.Web {
		results[i] = search.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Description,
			Position:    i + 1,
		}
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

func (p *firecrawlProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	results := make([]search.ExtractResult, 0, len(urls))
	for _, u := range urls {
		var out struct {
			Data struct {
				Markdown string `json:"markdown"`
				Metadata struct {
					SourceURL string `json:"sourceURL"`
					Title     string `json:"title"`
				} `json:"metadata"`
			} `json:"data"`
		}
		resp, err := p.Client.R().
			SetContext(ctx).
			SetAuthToken(p.APIKey).
			SetBody(map[string]any{"url": u, "formats": []string{"markdown"}}).
			Post(p.BaseURL + "/v2/scrape")
		if err != nil {
			results = append(results, search.ExtractResult{URL: u, Error: err.Error()})
			continue
		}
		if resp.IsError() {
			results = append(results, search.ExtractResult{URL: u, Error: p.ErrorFor(resp.StatusCode(), resp.String()).Error()})
			continue
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			results = append(results, search.ExtractResult{URL: u, Error: err.Error()})
			continue
		}
		results = append(results, search.ExtractResult{
			URL:     out.Data.Metadata.SourceURL,
			Title:   out.Data.Metadata.Title,
			Content: out.Data.Markdown,
		})
	}
	return &search.ExtractResponse{Success: true, Results: results}, nil
}
