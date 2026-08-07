package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/infa/task_relay/master/agent/search"
)

// tavilyFamily implements the Tavily-shaped wire protocol shared by the
// tavily, perplexity and gateway providers. The only differences are the
// auth style (api_key in body vs Bearer header) and optional search_depth.
type tavilyFamily struct {
	search.Base
	searchDepth string
	bearer      bool
}

func newTavilyFamily(name string, cfg *search.Config) search.Provider {
	base, ok := search.NewBase(name, cfg, search.BaseOpts{Search: true, Extract: true})
	if !ok {
		return nil
	}
	pc := search.ProviderConfigFor(cfg, name)
	bearer := name == "perplexity"
	if pc.IsBearer != nil {
		bearer = *pc.IsBearer
	}
	return &tavilyFamily{Base: *base, searchDepth: pc.SearchDepth, bearer: bearer}
}

func (p *tavilyFamily) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	body := map[string]any{
		"query":       query,
		"max_results": limit,
	}
	if p.searchDepth != "" {
		body["search_depth"] = p.searchDepth
	}
	if !p.bearer {
		body["api_key"] = p.APIKey
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	req := p.Client.R().SetContext(ctx).SetBody(body)
	if p.bearer {
		req.SetAuthToken(p.APIKey)
	}
	resp, err := req.Post(p.BaseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("%s search: %w", p.NameVal, err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("%s search: decode response: %w", p.NameVal, err)
	}

	results := make([]search.SearchResult, len(out.Results))
	for i, r := range out.Results {
		results[i] = search.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    i + 1,
		}
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

func (p *tavilyFamily) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("urls is required")
	}
	body := map[string]any{
		"urls":    urls,
		"api_key": p.APIKey,
	}

	var out struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	req := p.Client.R().SetContext(ctx).SetBody(body)
	if p.bearer {
		req.SetAuthToken(p.APIKey)
	}
	resp, err := req.Post(p.BaseURL + "/extract")
	if err != nil {
		return nil, fmt.Errorf("%s extract: %w", p.NameVal, err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return nil, fmt.Errorf("%s extract: decode response: %w", p.NameVal, err)
	}

	results := make([]search.ExtractResult, len(out.Results))
	for i, r := range out.Results {
		results[i] = search.ExtractResult{URL: r.URL, Title: r.Title, Content: r.Content}
	}
	return &search.ExtractResponse{Success: true, Results: results}, nil
}
