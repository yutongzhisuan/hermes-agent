package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMarshalSearchResponse(t *testing.T) {
	resp := &SearchResponse{
		Success: true,
		Results: []SearchResult{
			{Title: "Go", URL: "https://go.dev", Description: "Go language", Position: 1},
		},
	}
	out, err := marshalSearchResponse(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		Success bool `json:"success"`
		Data    struct {
			Web []SearchResult `json:"web"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !parsed.Success {
		t.Error("expected success")
	}
	if len(parsed.Data.Web) != 1 || parsed.Data.Web[0].URL != "https://go.dev" {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestWebSearchNoProvider(t *testing.T) {
	cfg := &SearchConfig{Enabled: new(true)}
	tools, err := BuildSearchTools(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools, got %d", len(tools))
	}
}

func TestWebSearchFakeProvider(t *testing.T) {
	cfg := &SearchConfig{
		Enabled: new(true),
		Providers: map[string]SearchProviderConfig{
			"tavily": {BaseURL: "http://t", APIKey: "k"},
		},
	}
	tools, err := BuildSearchTools(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

type fakeSearchProvider struct {
	fakeProvider
}

func (f *fakeSearchProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	return &SearchResponse{Success: true, Results: []SearchResult{{Title: query, URL: "https://x", Description: "ok", Position: 1}}}, nil
}
