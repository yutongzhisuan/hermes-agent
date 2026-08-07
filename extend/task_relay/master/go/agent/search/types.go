package search

import "encoding/json"

// SearchResult is one normalized web search hit.
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Position    int    `json:"position"`
}

// ExtractResult is one normalized page-extraction result.
type ExtractResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// SearchResponse is the normalized search output.
type SearchResponse struct {
	Success bool           `json:"success"`
	Results []SearchResult `json:"results,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// ExtractResponse is the normalized extract output.
type ExtractResponse struct {
	Success bool            `json:"success"`
	Results []ExtractResult `json:"results,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// StringOf coerces a JSON-decoded scalar to string.
func StringOf(v any) string {
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
