package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/agent/search"
)

func TestMarshalSearchResponse(t *testing.T) {
	resp := &search.SearchResponse{
		Success: true,
		Results: []search.SearchResult{
			{Title: "Go", URL: "https://go.dev", Description: "Go language", Position: 1},
		},
	}
	out, err := marshalSearchResponse(resp)
	require.NoError(t, err)

	var parsed struct {
		Success bool `json:"success"`
		Data    struct {
			Web []search.SearchResult `json:"web"`
		} `json:"data"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.True(t, parsed.Success)
	assert.Len(t, parsed.Data.Web, 1)
	assert.Equal(t, "https://go.dev", parsed.Data.Web[0].URL)
}

func TestWebSearchNoProvider(t *testing.T) {
	cfg := &search.Config{Enabled: search.BoolPtr(true)}
	tools, err := BuildSearchTools(cfg)
	require.NoError(t, err)
	assert.Empty(t, tools)
}

func TestWebSearchFakeProvider(t *testing.T) {
	cfg := &search.Config{
		Enabled: search.BoolPtr(true),
		Providers: map[string]search.ProviderConfig{
			"tavily": {BaseURL: "http://t", APIKey: "k"},
		},
	}
	tools, err := BuildSearchTools(cfg)
	require.NoError(t, err)
	assert.Len(t, tools, 2)
}

func TestWebSearchErrorResponse(t *testing.T) {
	out, err := marshalSearchResponse(search.SearchErr(assert.AnError))
	require.NoError(t, err)
	var parsed struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.False(t, parsed.Success)
	assert.NotEmpty(t, parsed.Error)
}
