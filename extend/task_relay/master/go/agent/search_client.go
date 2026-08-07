package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// tavilyClient calls a Tavily- or Perplexity-compatible search/extract HTTP API.
// It is used directly by the tavily, perplexity and gateway providers.
type tavilyClient struct {
	baseURL string
	apiKey  string
	bearer  bool
	http    *http.Client
}

func newTavilyClient(baseURL, apiKey string, bearer bool, timeout time.Duration) *tavilyClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &tavilyClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		bearer:  bearer,
		http:    &http.Client{Timeout: timeout},
	}
}

// SearchResultTavily is the raw result shape returned by Tavily/Perplexity /search.
type SearchResultTavily struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// ExtractResultTavily is the raw result shape returned by Tavily /extract.
type ExtractResultTavily struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (c *tavilyClient) Search(ctx context.Context, query string, limit int, searchDepth, timeRange, lang string) (*SearchResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("tavily client is nil")
	}
	body := map[string]any{
		"query":       query,
		"max_results": limit,
	}
	if searchDepth != "" {
		body["search_depth"] = searchDepth
	}
	if timeRange != "" {
		body["time_range"] = timeRange
	}
	if lang != "" {
		body["lang"] = lang
	}
	if !c.bearer {
		body["api_key"] = c.apiKey
	}

	data, err := c.postJSON(ctx, c.baseURL+"/search", body)
	if err != nil {
		return nil, err
	}

	results, err := extractTavilySearchResults(data)
	if err != nil {
		return nil, err
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    i + 1,
		}
	}
	return &SearchResponse{Success: true, Results: out}, nil
}

func extractTavilySearchResults(data map[string]any) ([]SearchResultTavily, error) {
	// Tavily returns {"results": [...]}. Perplexity returns the same shape.
	raw, ok := data["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("search api response missing results list")
	}
	out := make([]SearchResultTavily, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, SearchResultTavily{
			Title:   stringOf(m["title"]),
			URL:     stringOf(m["url"]),
			Content: stringOf(m["content"]),
		})
	}
	return out, nil
}

func (c *tavilyClient) Extract(ctx context.Context, urls []string, size, renderer string) (*ExtractResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("tavily client is nil")
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("urls is required")
	}
	body := map[string]any{
		"urls":    urls,
		"api_key": c.apiKey,
	}
	if size != "" {
		body["size"] = size
	}
	if renderer != "" {
		body["renderer"] = renderer
	}

	data, err := c.postJSON(ctx, c.baseURL+"/extract", body)
	if err != nil {
		return nil, err
	}

	results, err := extractTavilyExtractResults(data)
	if err != nil {
		return nil, err
	}

	out := make([]ExtractResult, len(results))
	for i, r := range results {
		out[i] = ExtractResult{
			URL:     r.URL,
			Title:   r.Title,
			Content: r.Content,
		}
	}
	return &ExtractResponse{Success: true, Results: out}, nil
}

func extractTavilyExtractResults(data map[string]any) ([]ExtractResultTavily, error) {
	raw, ok := data["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("extract api response missing results list")
	}
	out := make([]ExtractResultTavily, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, ExtractResultTavily{
			URL:     stringOf(m["url"]),
			Title:   stringOf(m["title"]),
			Content: stringOf(m["content"]),
		})
	}
	return out, nil
}

func (c *tavilyClient) postJSON(ctx context.Context, url string, body map[string]any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.bearer {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search api status %d: %s", resp.StatusCode, truncateRunes(string(raw), 512))
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("search api returned non-json body")
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return data, nil
}

// stringOf coerces a JSON-decoded scalar to string.
func stringOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
