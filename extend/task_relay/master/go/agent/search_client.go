package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SearchClient calls a Tavily- or Perplexity-compatible search/extract HTTP API.
type SearchClient struct {
	provider    string
	baseURL     string
	apiKey      string
	maxResults  int
	searchDepth string
	http        *http.Client
}

func NewSearchClient(cfg *SearchConfig) (*SearchClient, error) {
	if cfg == nil || !cfg.IsEnabled() {
		return nil, fmt.Errorf("search config is disabled or incomplete")
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = searchProviderTavily
	}
	switch provider {
	case searchProviderTavily, searchProviderPerplexity:
	default:
		return nil, fmt.Errorf("unsupported search provider %q (use tavily|perplexity)", cfg.Provider)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &SearchClient{
		provider:    provider,
		baseURL:     strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		apiKey:      strings.TrimSpace(cfg.APIKey),
		maxResults:  cfg.MaxResults,
		searchDepth: cfg.SearchDepth,
		http:        &http.Client{Timeout: timeout},
	}, nil
}

type searchRequestOpts struct {
	MaxResults  int
	SearchDepth string
	TimeRange   string
	Lang        string
}

func (c *SearchClient) Search(ctx context.Context, query string, opts searchRequestOpts) (string, error) {
	if c == nil {
		return "", fmt.Errorf("search client is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = c.maxResults
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	body := map[string]any{
		"query":       query,
		"max_results": maxResults,
	}
	depth := opts.SearchDepth
	if depth == "" {
		depth = c.searchDepth
	}
	if depth != "" {
		body["search_depth"] = depth
	}
	if opts.TimeRange != "" {
		body["time_range"] = opts.TimeRange
	}
	if opts.Lang != "" {
		body["lang"] = opts.Lang
	}
	if c.provider == searchProviderTavily {
		body["api_key"] = c.apiKey
	}
	return c.postJSON(ctx, c.baseURL+"/search", body, c.provider == searchProviderPerplexity)
}

type extractRequestOpts struct {
	URLs     []string
	URL      string
	Size     string
	Renderer string
}

func (c *SearchClient) Extract(ctx context.Context, opts extractRequestOpts) (string, error) {
	if c == nil {
		return "", fmt.Errorf("search client is nil")
	}
	body := map[string]any{}
	if len(opts.URLs) > 0 {
		body["urls"] = opts.URLs
	}
	if strings.TrimSpace(opts.URL) != "" {
		body["url"] = strings.TrimSpace(opts.URL)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("urls or url is required")
	}
	if opts.Size != "" {
		body["size"] = opts.Size
	}
	if opts.Renderer != "" {
		body["renderer"] = opts.Renderer
	}
	// Extract API is Tavily-shaped; always send body api_key. Perplexity provider
	// also sends Bearer for gateways that key off the Authorization header.
	body["api_key"] = c.apiKey
	return c.postJSON(ctx, c.baseURL+"/extract", body, c.provider == searchProviderPerplexity)
}

func (c *SearchClient) postJSON(ctx context.Context, url string, body map[string]any, bearer bool) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("search api status %d: %s", resp.StatusCode, truncateRunes(string(raw), 512))
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("search api returned non-json body")
	}
	return string(raw), nil
}
