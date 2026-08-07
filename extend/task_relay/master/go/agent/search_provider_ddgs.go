package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ddgsProvider scrapes the DuckDuckGo HTML endpoint. Search-only.
type ddgsProvider struct {
	cfg     SearchProviderConfig
	baseURL string
	http    httpDoer
}

func newDDGSProvider(cfg *SearchConfig) Provider {
	if cfg == nil {
		return nil
	}
	pc := providerConfig(cfg, "ddgs")
	if !providerEnabled("ddgs", pc) {
		return nil
	}
	// ddgs has a default base URL, so only build it when explicitly configured
	// (base_url set or enabled flag present). Otherwise an empty config would
	// silently activate it and break the "no providers configured" detection.
	if pc.BaseURL == "" && pc.Enabled == nil {
		return nil
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = "https://html.duckduckgo.com"
	}
	timeout := providerTimeout(cfg, pc)
	return &ddgsProvider{
		cfg:     pc,
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

func (p *ddgsProvider) Name() string          { return "ddgs" }
func (p *ddgsProvider) IsAvailable() bool     { return p.baseURL != "" }
func (p *ddgsProvider) SupportsSearch() bool  { return true }
func (p *ddgsProvider) SupportsExtract() bool { return false }

func (p *ddgsProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("ddgs provider is nil")
	}

	form := url.Values{}
	form.Set("q", query)
	form.Set("b", "1")
	form.Set("l", "wt-wt")
	form.Set("s", "0")
	form.Set("df", "")
	form.Set("kl", "wt-wt")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	raw, err := doGetBytes(ctx, p.http, req)
	if err != nil {
		return &SearchResponse{Success: false, Error: err.Error()}, nil
	}

	results := parseDDGSResults(raw)
	if limit > 0 && limit < len(results) {
		results = results[:limit]
	}
	return &SearchResponse{Success: true, Results: results}, nil
}

func (p *ddgsProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	return &ExtractResponse{Success: false, Error: "ddgs does not support extract"}, nil
}

// parseDDGSResults extracts result__a / result__snippet pairs from the DDG
// HTML endpoint, decodes the uddg redirect param and drops ad links (y.js).
func parseDDGSResults(raw []byte) []SearchResult {
	doc, err := html.Parse(strings.NewReader(string(raw)))
	if err != nil {
		return nil
	}
	var out []SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			class := attrOf(n, "class")
			switch {
			case strings.Contains(class, "result__a"):
				href := attrOf(n, "href")
				if strings.Contains(href, "y.js?") || strings.Contains(href, "ad_domain") {
					break
				}
				realURL := decodeUDDG(href)
				if realURL == "" {
					break
				}
				out = append(out, SearchResult{
					Title:    strings.TrimSpace(textOf(n)),
					URL:      realURL,
					Position: len(out) + 1,
				})
			case strings.Contains(class, "result__snippet"):
				if len(out) > 0 {
					out[len(out)-1].Description = strings.TrimSpace(textOf(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// decodeUDDG turns a DDG redirect URL (//duckduckgo.com/l/?uddg=<enc>&rut=...)
// into the real target URL.
func decodeUDDG(href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if u.Host == "" && strings.HasPrefix(href, "//") {
		u, err = url.Parse("https:" + href)
		if err != nil {
			return href
		}
	}
	target := u.Query().Get("uddg")
	if target == "" {
		return href
	}
	dec, err := url.QueryUnescape(target)
	if err != nil {
		return target
	}
	return dec
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
	return sb.String()
}
