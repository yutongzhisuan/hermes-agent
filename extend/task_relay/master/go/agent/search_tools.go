package agent

import (
	"context"
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
	client *SearchClient
}

// BuildSearchTools registers web_search and web_extract for an enabled SearchConfig.
func BuildSearchTools(cfg *SearchConfig) ([]tool.BaseTool, error) {
	if cfg == nil || !cfg.IsEnabled() {
		return nil, nil
	}
	client, err := NewSearchClient(cfg)
	if err != nil {
		return nil, err
	}
	host := &searchToolHost{client: client}
	searchTool, err := toolutils.InferTool(
		"web_search",
		"Search the web via the configured Tavily or Perplexity compatible API",
		host.webSearch,
	)
	if err != nil {
		return nil, fmt.Errorf("web_search: %w", err)
	}
	extractTool, err := toolutils.InferTool(
		"web_extract",
		"Extract webpage content via the Tavily-compatible extract API",
		host.webExtract,
	)
	if err != nil {
		return nil, fmt.Errorf("web_extract: %w", err)
	}
	return []tool.BaseTool{searchTool, extractTool}, nil
}

func (h *searchToolHost) webSearch(ctx context.Context, in WebSearchInput) (string, error) {
	return h.client.Search(ctx, in.Query, searchRequestOpts{
		MaxResults:  in.MaxResults,
		SearchDepth: in.SearchDepth,
		TimeRange:   in.TimeRange,
		Lang:        in.Lang,
	})
}

func (h *searchToolHost) webExtract(ctx context.Context, in WebExtractInput) (string, error) {
	return h.client.Extract(ctx, extractRequestOpts{
		URLs:     in.URLs,
		URL:      in.URL,
		Size:     in.Size,
		Renderer: in.Renderer,
	})
}
