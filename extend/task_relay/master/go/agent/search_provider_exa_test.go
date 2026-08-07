package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExaProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "exa-key" {
			t.Errorf("x-api-key = %q", got)
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
			"results": []map[string]any{
				{"url": "https://go.dev", "title": "Go", "highlights": []string{"The Go Programming Language"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newExaProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"exa": {BaseURL: server.URL, APIKey: "exa-key"},
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

func TestExaProviderExtract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/contents" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		resp := map[string]any{
			"results": []map[string]any{
				{
					"url":   "https://go.dev",
					"title": "Go",
					"text":  "Go is an open source programming language.",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := newExaProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"exa": {BaseURL: server.URL, APIKey: "exa-key"},
		},
	})
	resp, err := p.Extract(context.Background(), []string{"https://go.dev"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if len(resp.Results) != 1 || resp.Results[0].Content != "Go is an open source programming language." {
		t.Errorf("unexpected results: %+v", resp.Results)
	}
}
