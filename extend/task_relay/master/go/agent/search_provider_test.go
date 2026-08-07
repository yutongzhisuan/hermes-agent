package agent

import (
	"context"
	"testing"
)

func TestResolveProviderExplicit(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "tavily", search: true, extract: true, available: true},
		&fakeProvider{name: "searxng", search: true, extract: false, available: true},
	}

	p, err := ResolveProvider("searxng", "search", providers)
	if err != nil {
		t.Fatalf("explicit search: %v", err)
	}
	if p.Name() != "searxng" {
		t.Errorf("got %q", p.Name())
	}

	_, err = ResolveProvider("searxng", "extract", providers)
	if err == nil {
		t.Error("searxng should not support extract")
	}

	_, err = ResolveProvider("unknown", "search", providers)
	if err == nil {
		t.Error("unknown provider should error")
	}
}

func TestResolveProviderExplicitUnavailable(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "tavily", search: true, extract: true, available: true},
		&fakeProvider{name: "searxng", search: true, available: false},
	}

	p, err := ResolveProvider("searxng", "search", providers)
	if err != nil {
		t.Fatalf("explicit unavailable should still resolve to give precise error later: %v", err)
	}
	if p.Name() != "searxng" {
		t.Errorf("got %q", p.Name())
	}
}

func TestResolveProviderSingleAvailable(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "tavily", search: true, extract: true, available: false},
		&fakeProvider{name: "searxng", search: true, extract: false, available: true},
	}

	p, err := ResolveProvider("", "search", providers)
	if err != nil {
		t.Fatalf("single available: %v", err)
	}
	if p.Name() != "searxng" {
		t.Errorf("got %q", p.Name())
	}
}

func TestResolveProviderLegacyWalk(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "tavily", search: true, extract: true, available: false},
		&fakeProvider{name: "exa", search: true, extract: true, available: true},
		&fakeProvider{name: "searxng", search: true, extract: false, available: true},
	}

	p, err := ResolveProvider("", "search", providers)
	if err != nil {
		t.Fatalf("legacy walk: %v", err)
	}
	if p.Name() != "exa" {
		t.Errorf("legacy walk should prefer exa over searxng, got %q", p.Name())
	}
}

func TestResolveProviderNoAvailable(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "tavily", search: true, available: false},
	}

	_, err := ResolveProvider("", "search", providers)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveProviderLegacySkipsMissing(t *testing.T) {
	providers := []Provider{
		&fakeProvider{name: "searxng", search: true, available: true},
	}

	p, err := ResolveProvider("", "search", providers)
	if err != nil {
		t.Fatalf("legacy skip: %v", err)
	}
	if p.Name() != "searxng" {
		t.Errorf("got %q", p.Name())
	}
}

func TestBuildProviderRegistry(t *testing.T) {
	cfg := &SearchConfig{
		Enabled: new(true),
		Providers: map[string]SearchProviderConfig{
			"tavily":  {BaseURL: "http://t", APIKey: "k"},
			"searxng": {BaseURL: "http://s"},
		},
	}
	providers := BuildProviderRegistry(cfg)
	if len(providers) != 2 {
		t.Fatalf("expected 2 configured providers, got %d", len(providers))
	}
	if providers[0].Name() != "tavily" {
		t.Errorf("expected first provider tavily, got %q", providers[0].Name())
	}
	if providers[1].Name() != "searxng" {
		t.Errorf("expected second provider searxng, got %q", providers[1].Name())
	}
}

type fakeProvider struct {
	name      string
	search    bool
	extract   bool
	available bool
}

func (f *fakeProvider) Name() string          { return f.name }
func (f *fakeProvider) IsAvailable() bool     { return f.available }
func (f *fakeProvider) SupportsSearch() bool  { return f.search }
func (f *fakeProvider) SupportsExtract() bool { return f.extract }
func (f *fakeProvider) Search(ctx context.Context, query string, limit int) (*SearchResponse, error) {
	return nil, nil
}
func (f *fakeProvider) Extract(ctx context.Context, urls []string) (*ExtractResponse, error) {
	return nil, nil
}
