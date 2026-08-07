package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearxngProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %q", r.Method)
		}
		if got := r.URL.Query().Get("q"); got != "golang" {
			t.Errorf("q = %q", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("format = %q", got)
		}

		resp := map[string]any{
			"results": []map[string]any{
				{"title": "Go", "url": "https://go.dev", "content": "The Go Programming Language", "score": 0.9},
				{"title": "Go Wiki", "url": "https://github.com/golang/go/wiki", "content": "Wiki", "score": 0.5},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newSearxngProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"searxng": {BaseURL: server.URL},
		},
	})
	if p == nil {
		t.Fatal("expected provider")
	}
	if !p.IsAvailable() {
		t.Fatal("expected available")
	}
	if p.SupportsExtract() {
		t.Error("searxng should not support extract")
	}

	resp, err := p.Search(context.Background(), "golang", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].URL != "https://go.dev" {
		t.Errorf("url = %q", resp.Results[0].URL)
	}
}
