// Package providers implements the concrete search backends.
package providers

import "github.com/infa/task_relay/master/agent/search"

// BuildRegistry constructs the ordered provider list from configuration.
// Providers are registered in KnownProviders order so the resolver walk is
// deterministic.
func BuildRegistry(cfg *search.Config) []search.Provider {
	if cfg == nil {
		return nil
	}
	var out []search.Provider
	for _, name := range search.KnownProviders {
		if p := build(name, cfg); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func build(name string, cfg *search.Config) search.Provider {
	switch name {
	case "firecrawl":
		return newFirecrawl(cfg)
	case "parallel":
		return newParallel(cfg)
	case "tavily":
		return newTavilyFamily("tavily", cfg)
	case "perplexity":
		return newTavilyFamily("perplexity", cfg)
	case "gateway":
		return newTavilyFamily("gateway", cfg)
	case "exa":
		return newExa(cfg)
	case "searxng":
		return newSearxng(cfg)
	case "brave-free":
		return newBrave(cfg)
	case "ddgs":
		return newDDGS(cfg)
	default:
		return nil
	}
}
