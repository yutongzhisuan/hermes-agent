package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// httpDoer is satisfied by *http.Client.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// doPostJSON sends a JSON POST request and decodes the response into a map.
func doPostJSON(ctx context.Context, client httpDoer, url string, body any, headers map[string]string) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doJSONRequest(client, req)
}

// doGet sends a GET request and returns the raw response body.
func doGet(ctx context.Context, client httpDoer, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doGetBytes(ctx, client, req)
}

// doGetBytes performs a pre-built request and returns the raw response body.
func doGetBytes(ctx context.Context, client httpDoer, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d: %s", resp.StatusCode, truncateRunes(string(raw), 512))
	}
	return raw, nil
}

// doJSONRequest performs the request and decodes a JSON body.
func doJSONRequest(client httpDoer, req *http.Request) (map[string]any, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d: %s", resp.StatusCode, truncateRunes(string(raw), 512))
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("api returned non-json body")
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return data, nil
}
