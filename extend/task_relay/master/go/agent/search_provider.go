package agent

import (
	"context"
	"fmt"
	"strings"
)

// SearchResult is the normalized shape returned by every provider's Search.
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Position    int    `json:"position"`
}

// ExtractResult is the normalized shape returned by every provider's Extract.
// Errors are per-URL rather than raised so the tool can return a full list.
type ExtractResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// SearchResponse is the normalized web_search output.
type SearchResponse struct {
	Success bool           `json:"success"`
	Results []SearchResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

// ExtractResponse is the normalized web_extract output.
type ExtractResponse struct {
	Success bool            `json:"success"`
	Results []ExtractResult `json:"results"`
	Error   string          `json:"error,omitempty"`
}

// Provider is the common interface for web search/extract backends.
// Implementations must be cheap to construct; IsAvailable must not perform
// network I/O.
type Provider interface {
	Name() string
	IsAvailable() bool
	SupportsSearch() bool
	SupportsExtract() bool
	Search(ctx context.Context, query string, limit int) (*SearchResponse, error)
	Extract(ctx context.Context, urls []string) (*ExtractResponse, error)
}

// allProviderNames lists every provider the Go master knows about.
var allProviderNames = []string{
	"firecrawl",
	"parallel",
	"tavily",
	"perplexity",
	"gateway",
	"exa",
	"searxng",
	"brave-free",
	"ddgs",
}

// Legacy preference order matches the Python side in web_search_registry.py.
var legacyPreference = []string{
	"firecrawl",
	"parallel",
	"tavily",
	"exa",
	"searxng",
	"brave-free",
	"ddgs",
}

// providerNames returns the names of every provider in the registry.
func providerNames(providers []Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name()
	}
	return names
}

// findProvider returns the provider with the given name, or nil.
func findProvider(name string, providers []Provider) Provider {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, p := range providers {
		if strings.ToLower(p.Name()) == name {
			return p
		}
	}
	return nil
}

// capabilitySupported reports whether a provider supports the named capability.
func capabilitySupported(p Provider, capability string) bool {
	switch strings.ToLower(capability) {
	case "search":
		return p.SupportsSearch()
	case "extract":
		return p.SupportsExtract()
	default:
		return false
	}
}

// ResolveProvider selects the active provider for a capability.
//
// Resolution rules (mirror the Python side in agent/web_search_registry.py):
//
//  1. If configured is non-empty, return that provider by name even if it is
//     not available. This gives the user a precise downstream error message
//     (e.g. "searxng base_url not set") instead of silently switching backends.
//     If the named provider does not support the capability, return an error.
//  2. If exactly one registered provider supports the capability and is available,
//     use it.
//  3. Walk legacyPreference and return the first provider that supports the
//     capability and is available.
//  4. Return an error listing the available providers for this capability.
func ResolveProvider(configured string, capability string, providers []Provider) (Provider, error) {
	capability = strings.ToLower(capability)
	configured = strings.ToLower(strings.TrimSpace(configured))

	if configured != "" {
		p := findProvider(configured, providers)
		if p == nil {
			return nil, fmt.Errorf(
				"configured %s backend %q is not a known provider (available: %s)",
				capability, configured, strings.Join(providerNames(providers), ", "),
			)
		}
		if !capabilitySupported(p, capability) {
			return nil, fmt.Errorf(
				"provider %q does not support %s; choose one of: %s",
				configured, capability, strings.Join(supportingNames(capability, providers), ", "),
			)
		}
		return p, nil
	}

	available := supportingAvailable(capability, providers)
	if len(available) == 1 {
		return available[0], nil
	}

	for _, want := range legacyPreference {
		p := findProvider(want, providers)
		if p == nil {
			continue
		}
		if !capabilitySupported(p, capability) {
			continue
		}
		if p.IsAvailable() {
			return p, nil
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf(
			"no %s provider is available. Set search.%s_backend or configure providers in search.providers (%s)",
			capability, capability, strings.Join(providerNames(providers), ", "),
		)
	}

	// More than one available but none explicitly configured and legacy walk did
	// not pick one. List the available options so the user can disambiguate.
	return nil, fmt.Errorf(
		"multiple %s providers are available (%s); disambiguate with search.%s_backend or search.backend",
		capability, strings.Join(providerNames(available), ", "), capability,
	)
}

// supportingNames returns the names of providers that support a capability.
func supportingNames(capability string, providers []Provider) []string {
	var names []string
	for _, p := range providers {
		if capabilitySupported(p, capability) {
			names = append(names, p.Name())
		}
	}
	return names
}

// supportingAvailable returns providers that support a capability and are available.
func supportingAvailable(capability string, providers []Provider) []Provider {
	var out []Provider
	for _, p := range providers {
		if capabilitySupported(p, capability) && p.IsAvailable() {
			out = append(out, p)
		}
	}
	return out
}

// BuildProviderRegistry constructs the ordered provider list from configuration.
// Providers are registered in a deterministic order. Unconfigured providers
// are omitted, but explicit selection can surface precise configuration errors
// by matching a configured block that is incomplete.
func BuildProviderRegistry(cfg *SearchConfig) []Provider {
	if cfg == nil {
		return nil
	}
	var providers []Provider
	for _, name := range allProviderNames {
		if p := buildProvider(name, cfg); p != nil {
			providers = append(providers, p)
		}
	}
	return providers
}

// buildProvider creates a single provider from configuration when a matching
// provider block exists.
func buildProvider(name string, cfg *SearchConfig) Provider {
	switch name {
	case "firecrawl":
		return newFirecrawlProvider(cfg)
	case "parallel":
		return newParallelProvider(cfg)
	case "tavily":
		return newTavilyProvider(cfg)
	case "perplexity":
		return newPerplexityProvider(cfg)
	case "gateway":
		return newGatewayProvider(cfg)
	case "exa":
		return newExaProvider(cfg)
	case "searxng":
		return newSearxngProvider(cfg)
	case "brave-free":
		return newBraveProvider(cfg)
	case "ddgs":
		return newDDGSProvider(cfg)
	default:
		return nil
	}
}
