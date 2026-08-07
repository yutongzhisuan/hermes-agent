package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ddgsHTML = `<html><body>
<div class="result">
	<h2 class="result__title">
		<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=abc">Go</a>
	</h2>
	<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=abc">The Go language</a>
</div>
<div class="result">
	<h2 class="result__title">
		<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F&rut=def">Example</a>
	</h2>
	<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F&rut=def">An example domain</a>
</div>
<div class="result">
	<h2 class="result__title">
		<a class="result__a" href="//duckduckgo.com/y.js?ad=1">Sponsored</a>
	</h2>
</div>
</body></html>`

func TestDDGSSearchParsesHTML(t *testing.T) {
	var gotQ string
	var gotForm bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/html/", r.URL.Path)
		gotForm = r.Method == http.MethodPost
		gotQ = r.FormValue("q")
		w.Write([]byte(ddgsHTML))
	}))
	defer srv.Close()

	p := newDDGS(testConfig("ddgs", srv.URL, ""))
	require.NotNil(t, p)

	resp, err := p.Search(context.Background(), "golang", 5)
	require.NoError(t, err)
	assert.True(t, gotForm)
	assert.Equal(t, "golang", gotQ)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "Go", resp.Results[0].Title)
	assert.Equal(t, "https://go.dev/", resp.Results[0].URL)
	assert.Equal(t, "The Go language", resp.Results[0].Description)
	assert.Equal(t, "Example", resp.Results[1].Title)
	assert.Equal(t, "https://example.com/", resp.Results[1].URL)
}

func TestDDGSNoExtract(t *testing.T) {
	p := newDDGS(testConfig("ddgs", "http://x", ""))
	_, err := p.Extract(context.Background(), []string{"https://a"})
	require.Error(t, err)
}
