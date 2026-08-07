package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirecrawlSearch(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/search", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		decodeBody(t, r, &gotBody)
		w.Write([]byte(`{"success":true,"data":{"web":[
			{"title":"A","url":"https://a","description":"dA"}
		]}}`))
	}))
	defer srv.Close()

	p := newFirecrawl(testConfig("firecrawl", srv.URL, "k"))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "q", 5)
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "A", resp.Results[0].Title)
	assert.Equal(t, "https://a", resp.Results[0].URL)
	assert.Equal(t, "Bearer k", gotAuth)
	assert.Equal(t, "q", gotBody["query"])
	assert.Equal(t, float64(5), gotBody["limit"])
}

func TestFirecrawlExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/scrape", r.URL.Path)
		w.Write([]byte(`{"success":true,"data":{
			"markdown":"# Hello",
			"metadata":{"sourceURL":"https://a","title":"A"}
		}}`))
	}))
	defer srv.Close()

	p := newFirecrawl(testConfig("firecrawl", srv.URL, "k"))
	resp, err := p.Extract(context.Background(), []string{"https://a"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "# Hello", resp.Results[0].Content)
	assert.Equal(t, "https://a", resp.Results[0].URL)
	assert.Equal(t, "A", resp.Results[0].Title)
}

func TestFirecrawlExtractPerURLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	p := newFirecrawl(testConfig("firecrawl", srv.URL, "k"))
	resp, err := p.Extract(context.Background(), []string{"https://a"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "https://a", resp.Results[0].URL)
	assert.Contains(t, resp.Results[0].Error, "500")
}
