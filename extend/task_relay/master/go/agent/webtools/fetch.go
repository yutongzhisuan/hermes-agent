package webtools

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"golang.org/x/net/html"

	"github.com/infa/task_relay/master/agent/policy"
)

type FetchTool struct {
	deps *Deps
}

func NewFetchTool(deps *Deps) *FetchTool { return &FetchTool{deps: deps} }

type FetchInput struct {
	URL string `json:"url" jsonschema:"required,description=HTTP(S) URL to fetch"`
}

type FetchOutput struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}

func (f *FetchTool) Run(ctx context.Context, in FetchInput) (FetchOutput, error) {
	u, err := f.deps.validateURL(in.URL)
	if err != nil {
		if auditErr := f.deps.auditDenied("web_fetch", in.URL, policy.Deny.String()); auditErr != nil {
			return FetchOutput{}, auditErr
		}
		return FetchOutput{}, err
	}

	client := &http.Client{
		Transport: secureTransport(f.deps.AllowPrivateNetworks),
		Timeout:   f.deps.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := f.deps.validateURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return FetchOutput{}, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = f.deps.auditOp("web_fetch", in.URL, "", -1, err)
		return FetchOutput{}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("fetch %s: unexpected status %d", in.URL, resp.StatusCode)
		_ = f.deps.auditOp("web_fetch", in.URL, "", resp.StatusCode, err)
		return FetchOutput{}, err
	}

	body, truncated, err := readCapped(resp.Body, f.deps.MaxBytes)
	if err != nil {
		_ = f.deps.auditOp("web_fetch", in.URL, "", -1, err)
		return FetchOutput{}, fmt.Errorf("read body: %w", err)
	}

	content, err := renderBody(resp.Header.Get("Content-Type"), body)
	if err != nil {
		_ = f.deps.auditOp("web_fetch", in.URL, "", resp.StatusCode, err)
		return FetchOutput{}, err
	}

	finalURL := resp.Request.URL.String()
	out := FetchOutput{
		URL:        finalURL,
		StatusCode: resp.StatusCode,
		Content:    content,
		Truncated:  truncated,
	}
	if err := f.deps.auditOp("web_fetch", finalURL, content, 0, nil); err != nil {
		return FetchOutput{}, err
	}
	return out, nil
}

// readCapped reads at most max+1 bytes; truncated is true when the body
// exceeded max, in which case the result is cut to max bytes.
func readCapped(r io.Reader, max int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

func renderBody(contentType string, body []byte) (string, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	switch {
	case mediaType == "text/html":
		return htmlToText(body)
	case mediaType == "application/json" || strings.HasPrefix(mediaType, "text/"):
		return string(body), nil
	default:
		return "", fmt.Errorf("unsupported content type %q", contentType)
	}
}

// htmlToText extracts visible text: script/style subtrees are skipped and
// whitespace is collapsed.
func htmlToText(body []byte) (string, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " "), nil
}
