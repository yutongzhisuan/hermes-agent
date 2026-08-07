package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/search"
)

func TestTavilySearchBodyAuth(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search", r.URL.Path)
		decodeBody(t, r, &gotBody)
		assert.Equal(t, "", r.Header.Get("Authorization"))
		w.Write([]byte(`{"results":[{"title":"T","url":"https://t","content":"C"}]}`))
	}))
	defer srv.Close()

	cfg := &search.Config{Providers: map[string]search.ProviderConfig{
		"tavily": {BaseURL: srv.URL, APIKey: "k"},
	}}
	p := newTavilyFamily("tavily", cfg)
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 3)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "T", resp.Results[0].Title)
	assert.Equal(t, "C", resp.Results[0].Description)
	assert.Equal(t, "k", gotBody["api_key"])
	assert.Equal(t, float64(3), gotBody["max_results"])
	assert.Equal(t, "q", gotBody["query"])
}

func TestPerplexitySearchBearerAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	cfg := &search.Config{Providers: map[string]search.ProviderConfig{
		"perplexity": {BaseURL: srv.URL, APIKey: "k"},
	}}
	p := newTavilyFamily("perplexity", cfg)
	require.NotNil(t, p)
	_, err := p.Search(context.Background(), "q", 5)
	require.NoError(t, err)
	assert.Equal(t, "Bearer k", gotAuth)
}

func TestTavilyExtract(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/extract", r.URL.Path)
		decodeBody(t, r, &gotBody)
		w.Write([]byte(`{"results":[{"url":"https://t","title":"T","content":"C"}]}`))
	}))
	defer srv.Close()

	cfg := &search.Config{Providers: map[string]search.ProviderConfig{
		"tavily": {BaseURL: srv.URL, APIKey: "k"},
	}}
	p := newTavilyFamily("tavily", cfg)
	resp, err := p.Extract(context.Background(), []string{"https://t"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "C", resp.Results[0].Content)
	assert.Equal(t, []any{"https://t"}, gotBody["urls"])
}

func TestTavilyErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	cfg := &search.Config{Providers: map[string]search.ProviderConfig{
		"tavily": {BaseURL: srv.URL, APIKey: "bad"},
	}}
	p := newTavilyFamily("tavily", cfg)
	_, err := p.Search(context.Background(), "q", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}
