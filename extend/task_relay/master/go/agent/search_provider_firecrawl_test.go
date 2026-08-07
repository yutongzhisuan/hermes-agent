package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirecrawlProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fc-key" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req["query"] != "golang" {
			t.Errorf("query = %v", req["query"])
		}

		resp := map[string]any{
			"success": true,
			"data": map[string]any{
				"web": []map[string]any{
					{"title": "Go", "url": "https://go.dev", "description": "The Go Programming Language"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newFirecrawlProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"firecrawl": {BaseURL: server.URL, APIKey: "fc-key"},
		},
	})
	if p == nil {
		t.Fatal("expected provider")
	}
	if !p.IsAvailable() {
		t.Fatal("expected available")
	}

	resp, err := p.Search(context.Background(), "golang", 5)
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
	if resp.Results[0].Description != "The Go Programming Language" {
		t.Errorf("description = %q", resp.Results[0].Description)
	}
}

func TestFirecrawlProviderDefaultsToV2Base(t *testing.T) {
	p := newFirecrawlProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"firecrawl": {APIKey: "fc-key"},
		},
	})
	if p == nil {
		t.Fatal("expected provider")
	}
	if p.(*firecrawlProvider).baseURL != "https://api.firecrawl.dev" {
		t.Errorf("baseURL = %q", p.(*firecrawlProvider).baseURL)
	}
}

func TestFirecrawlProviderExtract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/scrape" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req["url"] != "https://go.dev" {
			t.Errorf("url = %v", req["url"])
		}

		resp := map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "Go is an open source programming language.",
				"metadata": map[string]any{"title": "Go", "sourceURL": "https://go.dev"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newFirecrawlProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"firecrawl": {BaseURL: server.URL, APIKey: "fc-key"},
		},
	})
	resp, err := p.Extract(context.Background(), []string{"https://go.dev"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Go" {
		t.Errorf("title = %q", resp.Results[0].Title)
	}
	if resp.Results[0].Content != "Go is an open source programming language." {
		t.Errorf("content = %q", resp.Results[0].Content)
	}
}
