package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// WebSearchInput is the agent tool input for web_search.
type WebSearchInput struct {
	Query       string `json:"query" jsonschema:"description=Search query"`
	MaxResults  int    `json:"max_results,omitempty" jsonschema:"description=Max results 1-20"`
	SearchDepth string `json:"search_depth,omitempty" jsonschema:"description=Tavily search_depth hint basic|fast|advanced"`
	TimeRange   string `json:"time_range,omitempty" jsonschema:"description=Recency filter hour|day|week|month|year"`
	Lang        string `json:"lang,omitempty" jsonschema:"description=Language code e.g. en zh"`
}

// WebExtractInput is the agent tool input for web_extract.
type WebExtractInput struct {
	URLs     []string `json:"urls,omitempty" jsonschema:"description=Batch URLs to extract (max 10)"`
	URL      string   `json:"url,omitempty" jsonschema:"description=Single URL paginated extract mode"`
	Size     string   `json:"size,omitempty" jsonschema:"description=Page size for single-URL mode s|m|l|f"`
	Renderer string   `json:"renderer,omitempty" jsonschema:"description=Renderer auto|light|crw|stealth"`
}

type searchToolHost struct {
	cfg       *SearchConfig
	providers []Provider
}

// BuildSearchTools registers web_search and web_extract for an enabled SearchConfig.
func BuildSearchTools(cfg *SearchConfig) ([]tool.BaseTool, error) {
	if cfg == nil || !cfg.IsEnabled() {
		return nil, nil
	}
	host := &searchToolHost{
		cfg:       cfg,
		providers: BuildProviderRegistry(cfg),
	}
	if len(host.providers) == 0 {
		return nil, nil
	}

	searchDesc := fmt.Sprintf(
		"Search the web via the configured provider (%s). Set search.search_backend to pick a specific provider.",
		supportedList(host.providers, capabilitySearch),
	)
	searchTool, err := toolutils.InferTool("web_search", searchDesc, host.webSearch)
	if err != nil {
		return nil, fmt.Errorf("web_search: %w", err)
	}

	extractDesc := fmt.Sprintf(
		"Extract webpage content via the configured extract-capable provider (%s). Set search.extract_backend to pick a specific provider.",
		supportedList(host.providers, capabilityExtract),
	)
	extractTool, err := toolutils.InferTool("web_extract", extractDesc, host.webExtract)
	if err != nil {
		return nil, fmt.Errorf("web_extract: %w", err)
	}

	return []tool.BaseTool{searchTool, extractTool}, nil
}

func (h *searchToolHost) webSearch(ctx context.Context, in WebSearchInput) (string, error) {
	provider, err := ResolveProvider(h.cfg.SearchBackend, capabilitySearch, h.providers)
	if err != nil {
		return marshalSearchResponse(&SearchResponse{Success: false, Error: err.Error()})
	}
	if provider == nil {
		return marshalSearchResponse(&SearchResponse{Success: false, Error: "no search provider configured"})
	}

	limit := in.MaxResults
	if limit <= 0 {
		limit = h.cfg.MaxResults
	}
	if limit <= 0 {
		limit = 5
	}

	resp, err := provider.Search(ctx, in.Query, limit)
	if err != nil {
		return marshalSearchResponse(&SearchResponse{Success: false, Error: err.Error()})
	}
	return marshalSearchResponse(resp)
}

func (h *searchToolHost) webExtract(ctx context.Context, in WebExtractInput) (string, error) {
	provider, err := ResolveProvider(h.cfg.ExtractBackend, capabilityExtract, h.providers)
	if err != nil {
		// Fallback to shared backend when extract_backend is unset.
		if h.cfg.Backend != "" {
			provider, err = ResolveProvider(h.cfg.Backend, capabilityExtract, h.providers)
		}
	}
	if err != nil {
		return marshalExtractResponse(&ExtractResponse{Success: false, Error: err.Error()})
	}
	if provider == nil {
		return marshalExtractResponse(&ExtractResponse{Success: false, Error: "no extract provider configured"})
	}

	urls := in.URLs
	if in.URL != "" {
		urls = append(urls, in.URL)
	}
	if len(urls) == 0 {
		return marshalExtractResponse(&ExtractResponse{Success: false, Error: "urls or url is required"})
	}

	resp, err := provider.Extract(ctx, urls)
	if err != nil {
		return marshalExtractResponse(&ExtractResponse{Success: false, Error: err.Error()})
	}
	return marshalExtractResponse(resp)
}

func marshalSearchResponse(resp *SearchResponse) (string, error) {
	out := struct {
		Success bool        `json:"success"`
		Data    *searchData `json:"data,omitempty"`
		Error   string      `json:"error,omitempty"`
	}{
		Success: resp.Success,
		Error:   resp.Error,
	}
	if resp.Success {
		out.Data = &searchData{Web: resp.Results}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

type searchData struct {
	Web []SearchResult `json:"web"`
}

func marshalExtractResponse(resp *ExtractResponse) (string, error) {
	out := struct {
		Success bool            `json:"success"`
		Results []ExtractResult `json:"results,omitempty"`
		Error   string          `json:"error,omitempty"`
	}{
		Success: resp.Success,
		Results: resp.Results,
		Error:   resp.Error,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func supportedList(providers []Provider, capability string) string {
	names := supportingNames(capability, providers)
	if len(names) == 0 {
		return "none"
	}
	return joinNames(names)
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	if len(names) == 2 {
		return names[0] + " and " + names[1]
	}
	return fmt.Sprintf("%s, and %s", joinNames(names[:len(names)-1]), names[len(names)-1])
}
