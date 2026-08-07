package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearxngSearchSortsByScore(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search", r.URL.Path)
		gotQuery = r.URL.Query().Get("q")
		assert.Equal(t, "json", r.URL.Query().Get("format"))
		w.Write([]byte(`{"results":[
			{"title":"low","url":"https://l","content":"c","score":0.1},
			{"title":"high","url":"https://h","content":"c","score":0.9},
			{"title":"mid","url":"https://m","content":"c","score":0.5}
		]}`))
	}))
	defer srv.Close()

	p := newSearxng(testConfig("searxng", srv.URL, ""))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 2)
	require.NoError(t, err)
	assert.Equal(t, "q", gotQuery)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "high", resp.Results[0].Title)
	assert.Equal(t, "mid", resp.Results[1].Title)
}

func TestSearxngNoExtract(t *testing.T) {
	p := newSearxng(testConfig("searxng", "http://x", ""))
	_, err := p.Extract(context.Background(), []string{"https://a"})
	require.Error(t, err)
}

func TestBraveSearch(t *testing.T) {
	var gotToken, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/res/v1/web/search", r.URL.Path)
		gotToken = r.Header.Get("X-Subscription-Token")
		gotCount = r.URL.Query().Get("count")
		w.Write([]byte(`{"web":{"results":[
			{"title":"A","url":"https://a","description":"dA"}
		]}}`))
	}))
	defer srv.Close()

	p := newBrave(testConfig("brave-free", srv.URL, "k"))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 5)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "A", resp.Results[0].Title)
	assert.Equal(t, "k", gotToken)
	assert.Equal(t, "5", gotCount)
}

func TestBraveCountCappedAt20(t *testing.T) {
	var gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCount = r.URL.Query().Get("count")
		w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()

	p := newBrave(testConfig("brave-free", srv.URL, "k"))
	_, err := p.Search(context.Background(), "q", 50)
	require.NoError(t, err)
	assert.Equal(t, "20", gotCount)
}

func TestBraveNoExtract(t *testing.T) {
	p := newBrave(testConfig("brave-free", "http://x", "k"))
	_, err := p.Extract(context.Background(), []string{"https://a"})
	require.Error(t, err)
}
