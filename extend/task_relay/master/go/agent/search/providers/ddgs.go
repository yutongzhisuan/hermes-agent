package providers

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/infa/task_relay/master/agent/search"
	"golang.org/x/net/html"
)

// ddgsProvider scrapes DuckDuckGo HTML search results (search only, no key).
type ddgsProvider struct {
	search.Base
}

func newDDGS(cfg *search.Config) search.Provider {
	base, ok := search.NewBase("ddgs", cfg, search.BaseOpts{
		DefaultBaseURL: "https://html.duckduckgo.com",
		Search:         true,
	})
	if !ok {
		return nil
	}
	return &ddgsProvider{Base: *base}
}

func (p *ddgsProvider) Search(ctx context.Context, query string, limit int) (*search.SearchResponse, error) {
	resp, err := p.Client.R().
		SetContext(ctx).
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36").
		SetFormData(map[string]string{"q": query, "b": "1", "l": "wt-wt", "s": "0", "df": ""}).
		Post(p.BaseURL + "/html/")
	if err != nil {
		return nil, fmt.Errorf("ddgs search: %w", err)
	}
	if resp.IsError() {
		return nil, p.ErrorFor(resp.StatusCode(), resp.String())
	}

	results, err := parseDDGSHTML(resp.Body(), limit)
	if err != nil {
		return nil, fmt.Errorf("ddgs parse: %w", err)
	}
	return &search.SearchResponse{Success: true, Results: results}, nil
}

func (p *ddgsProvider) Extract(ctx context.Context, urls []string) (*search.ExtractResponse, error) {
	return nil, fmt.Errorf("ddgs does not support extraction")
}

func parseDDGSHTML(body []byte, limit int) ([]search.SearchResult, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	var results []search.SearchResult
	var current *search.SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			class := attrOf(n, "class")
			href := attrOf(n, "href")
			switch {
			case hasClass(class, "result__a"):
				if strings.Contains(href, "y.js?") {
					break // sponsored/ad link
				}
				results = append(results, search.SearchResult{
					Title:    textOf(n),
					URL:      decodeUDDG(href),
					Position: len(results) + 1,
				})
				current = &results[len(results)-1]
			case hasClass(class, "result__snippet"):
				if current != nil {
					current.Description = textOf(n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func hasClass(class, want string) bool {
	for _, c := range strings.Fields(class) {
		if c == want {
			return true
		}
	}
	return false
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// decodeUDDG extracts the real target from a DDG redirect href:
// //duckduckgo.com/l/?uddg=<urlencoded>&rut=...
func decodeUDDG(href string) string {
	if !strings.Contains(href, "uddg=") {
		return href
	}
	rest := href[strings.Index(href, "uddg=")+len("uddg="):]
	if amp := strings.Index(rest, "&"); amp >= 0 {
		rest = rest[:amp]
	}
	if dec, err := url.QueryUnescape(rest); err == nil {
		return dec
	}
	return rest
}
