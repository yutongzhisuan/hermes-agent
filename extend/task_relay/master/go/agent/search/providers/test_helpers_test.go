package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/infa/task_relay/master/agent/search"
)

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode body %q: %v", string(raw), err)
	}
}

func testConfig(name, baseURL, apiKey string) *search.Config {
	cfg := &search.Config{}
	if cfg.Providers == nil {
		cfg.Providers = map[string]search.ProviderConfig{}
	}
	cfg.Providers[name] = search.ProviderConfig{BaseURL: baseURL, APIKey: apiKey}
	return cfg
}
