package search

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	name    string
	avail   bool
	search  bool
	extract bool
}

func (s *stubProvider) Name() string          { return s.name }
func (s *stubProvider) IsAvailable() bool     { return s.avail }
func (s *stubProvider) SupportsSearch() bool  { return s.search }
func (s *stubProvider) SupportsExtract() bool { return s.extract }
func (s *stubProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	return &SearchResponse{Success: true, Results: []SearchResult{{Title: query, URL: "https://x", Position: 1}}}, nil
}
func (s *stubProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	return &ExtractResponse{Success: true}, nil
}

func provider(name string, avail bool, caps ...string) *stubProvider {
	p := &stubProvider{name: name, avail: avail}
	for _, c := range caps {
		switch c {
		case CapabilitySearch:
			p.search = true
		case CapabilityExtract:
			p.extract = true
		}
	}
	return p
}

func TestResolveExplicitConfig(t *testing.T) {
	providers := []Provider{
		provider("tavily", true, CapabilitySearch, CapabilityExtract),
		provider("exa", true, CapabilitySearch),
	}
	p, err := ResolveProvider("tavily", CapabilitySearch, providers)
	require.NoError(t, err)
	assert.Equal(t, "tavily", p.Name())
}

func TestResolveExplicitUnconfigured(t *testing.T) {
	providers := []Provider{provider("tavily", true, CapabilitySearch)}
	_, err := ResolveProvider("firecrawl", CapabilitySearch, providers)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestResolveExplicitWrongCapability(t *testing.T) {
	providers := []Provider{provider("searxng", true, CapabilitySearch)}
	_, err := ResolveProvider("searxng", CapabilityExtract, providers)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support")
}

func TestResolveSingleAvailable(t *testing.T) {
	providers := []Provider{
		provider("tavily", false, CapabilitySearch),
		provider("exa", true, CapabilitySearch, CapabilityExtract),
		provider("searxng", false, CapabilitySearch),
	}
	p, err := ResolveProvider("", CapabilitySearch, providers)
	require.NoError(t, err)
	assert.Equal(t, "exa", p.Name())
}

func TestResolveLegacyPreferenceOrder(t *testing.T) {
	providers := []Provider{
		provider("ddgs", true, CapabilitySearch),
		provider("tavily", true, CapabilitySearch, CapabilityExtract),
		provider("searxng", true, CapabilitySearch),
	}
	p, err := ResolveProvider("", CapabilitySearch, providers)
	require.NoError(t, err)
	assert.Equal(t, "tavily", p.Name()) // firecrawl/parallel absent; tavily wins over exa/searxng/ddgs
}

func TestResolveMultipleAvailableNeedsConfig(t *testing.T) {
	// Two available providers both outside LegacyPreference order would need
	// explicit config; with our fixed preference walk one always wins, so
	// craft a case where preference walk finds nothing.
	providers := []Provider{provider("x1", true, CapabilitySearch), provider("x2", true, CapabilitySearch)}
	_, err := ResolveProvider("", CapabilitySearch, providers)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple providers available")
}

func TestResolveNoneAvailable(t *testing.T) {
	providers := []Provider{provider("tavily", false, CapabilitySearch)}
	p, err := ResolveProvider("", CapabilitySearch, providers)
	require.NoError(t, err)
	assert.Nil(t, p)
}

func TestSupportingNames(t *testing.T) {
	providers := []Provider{
		provider("tavily", true, CapabilitySearch, CapabilityExtract),
		provider("searxng", true, CapabilitySearch),
		provider("exa", true, CapabilitySearch, CapabilityExtract),
	}
	assert.Equal(t, []string{"tavily", "exa"}, SupportingNames(CapabilityExtract, providers))
	assert.Equal(t, "tavily and exa", JoinNames([]string{"tavily", "exa"}))
	assert.Equal(t, "tavily, exa, and searxng", JoinNames([]string{"tavily", "exa", "searxng"}))
}
