package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParallelProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "parallel-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("parallel-beta"); got != parallelBetaHeader {
			t.Errorf("parallel-beta = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if req["mode"] != "one-shot" {
			t.Errorf("mode = %v", req["mode"])
		}

		resp := map[string]any{
			"search_results": []map[string]any{
				{"url": "https://go.dev", "title": "Go", "snippet": "The Go Programming Language"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newParallelProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"parallel": {BaseURL: server.URL, APIKey: "parallel-key"},
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

func TestParallelProviderExtractIncludesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/extract" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		resp := map[string]any{
			"results": []map[string]any{
				{"url": "https://go.dev", "title": "Go", "content": "Go is an open source programming language."},
			},
			"errors": []map[string]any{
				{"url": "https://broken.example", "error": "404"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newParallelProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"parallel": {BaseURL: server.URL, APIKey: "parallel-key"},
		},
	})
	resp, err := p.Extract(context.Background(), []string{"https://go.dev", "https://broken.example"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results (1 ok + 1 error), got %d", len(resp.Results))
	}
	var okCount, errCount int
	for _, r := range resp.Results {
		if r.Error != "" {
			errCount++
			if r.URL != "https://broken.example" {
				t.Errorf("error result url = %q", r.URL)
			}
		} else {
			okCount++
		}
	}
	if okCount != 1 || errCount != 1 {
		t.Errorf("ok=%d err=%d, want 1/1", okCount, errCount)
	}
}
