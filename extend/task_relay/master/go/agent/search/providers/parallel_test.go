package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParallelSearchHeadersAndBody(t *testing.T) {
	var gotBody map[string]any
	var gotKey, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta/search", r.URL.Path)
		gotKey = r.Header.Get("x-api-key")
		gotBeta = r.Header.Get("parallel-beta")
		decodeBody(t, r, &gotBody)
		w.Write([]byte(`{"search_results":[{"title":"A","url":"https://a","snippet":"sA"}]}`))
	}))
	defer srv.Close()

	p := newParallel(testConfig("parallel", srv.URL, "k"))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 4)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "A", resp.Results[0].Title)
	assert.Equal(t, "sA", resp.Results[0].Description)
	assert.Equal(t, "k", gotKey)
	assert.Equal(t, parallelBetaHeader, gotBeta)
	assert.Equal(t, []any{"q"}, gotBody["search_queries"])
	assert.Equal(t, "fast", gotBody["mode"])
	assert.Equal(t, float64(4), gotBody["max_results"])
}

func TestParallelExtractErrorsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta/extract", r.URL.Path)
		w.Write([]byte(`{
			"results":[{"url":"https://ok","title":"OK","content":"c"}],
			"errors":[{"url":"https://bad","error":"page blocked"}]
		}`))
	}))
	defer srv.Close()

	p := newParallel(testConfig("parallel", srv.URL, "k"))
	resp, err := p.Extract(context.Background(), []string{"https://ok", "https://bad"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "c", resp.Results[0].Content)
	assert.Equal(t, "page blocked", resp.Results[1].Error)
}
