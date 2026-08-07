package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ddgsSampleHTML = `<!DOCTYPE html>
<html><body>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=abc123">The Go Programming Language</a>
  </h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F">Go is an open source programming language.</a>
</div>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a class="result__a" href="//duckduckgo.com/y.js?ad_provider=foo">Sponsored Ad</a>
  </h2>
</div>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title">
    <a class="result__a" href="https://example.org/direct">Direct Link</a>
  </h2>
</div>
</body></html>`

func TestDDGSProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/html/" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("bad form: %v", err)
		}
		if r.Form.Get("q") != "golang" {
			t.Errorf("q = %q", r.Form.Get("q"))
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(ddgsSampleHTML))
	}))
	defer server.Close()

	p := newDDGSProvider(&SearchConfig{
		Providers: map[string]SearchProviderConfig{
			"ddgs": {BaseURL: server.URL},
		},
	})
	if p == nil {
		t.Fatal("expected provider")
	}
	if !p.IsAvailable() {
		t.Fatal("expected available")
	}
	if p.SupportsExtract() {
		t.Error("ddgs should be search-only")
	}

	resp, err := p.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	// Ad link (y.js) must be filtered out; direct link preserved with uddg decoded.
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(resp.Results), resp.Results)
	}
	first := resp.Results[0]
	if first.URL != "https://go.dev/" {
		t.Errorf("url = %q, want decoded uddg", first.URL)
	}
	if first.Title != "The Go Programming Language" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Description != "Go is an open source programming language." {
		t.Errorf("description = %q", first.Description)
	}
	if resp.Results[1].URL != "https://example.org/direct" {
		t.Errorf("url = %q", resp.Results[1].URL)
	}
}

func TestDDGSUndecodableRedirect(t *testing.T) {
	got := decodeUDDG("//duckduckgo.com/l/?uddg=%ZZbroken&rut=x")
	if !strings.Contains(got, "%ZZbroken") {
		t.Errorf("unexpected decode result %q", got)
	}
}

func TestDDGSParseNoResults(t *testing.T) {
	got := parseDDGSResults([]byte("<html><body><p>nothing here</p></body></html>"))
	if len(got) != 0 {
		t.Errorf("expected 0 results, got %d", len(got))
	}
}
