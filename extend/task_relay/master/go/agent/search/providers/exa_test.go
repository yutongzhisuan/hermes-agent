package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExaSearch(t *testing.T) {
	var gotBody map[string]any
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search", r.URL.Path)
		gotKey = r.Header.Get("x-api-key")
		decodeBody(t, r, &gotBody)
		w.Write([]byte(`{"results":[
			{"url":"https://a","title":"A","highlights":["hA1","hA2"]}
		]}`))
	}))
	defer srv.Close()

	p := newExa(testConfig("exa", srv.URL, "k"))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 3)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "A", resp.Results[0].Title)
	assert.Equal(t, "hA1", resp.Results[0].Description)
	assert.Equal(t, "k", gotKey)
	assert.Equal(t, float64(3), gotBody["numResults"])
}

func TestExaExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/contents", r.URL.Path)
		w.Write([]byte(`{"results":[{"url":"https://a","title":"A","text":"body"}]}`))
	}))
	defer srv.Close()

	p := newExa(testConfig("exa", srv.URL, "k"))
	resp, err := p.Extract(context.Background(), []string{"https://a"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "body", resp.Results[0].Content)
}
