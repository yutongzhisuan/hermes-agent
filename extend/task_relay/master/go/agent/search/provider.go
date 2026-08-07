package search

import (
	"context"
	"fmt"
	"strings"
)

// Provider is one web search/extract backend.
type Provider interface {
	Name() string
	IsAvailable() bool
	SupportsSearch() bool
	SupportsExtract() bool
	Search(ctx context.Context, query string, limit int) (*SearchResponse, error)
	Extract(ctx context.Context, urls []string) (*ExtractResponse, error)
}

// Capability reports whether p supports the named capability.
func Capability(p Provider, capability string) bool {
	switch capability {
	case CapabilitySearch:
		return p.SupportsSearch()
	case CapabilityExtract:
		return p.SupportsExtract()
	default:
		return false
	}
}

// ResolveProvider picks the provider for a capability following the Python-side
// priority: explicit config > only available > legacy preference walk.
// Returns (nil, nil) when nothing is configured for the capability.
func ResolveProvider(configured string, capability string, providers []Provider) (Provider, error) {
	// 1. Explicit configuration.
	if configured != "" {
		for _, p := range providers {
			if p.Name() == configured {
				if !Capability(p, capability) {
					return nil, fmt.Errorf("provider %q does not support %s", configured, capability)
				}
				return p, nil
			}
		}
		return nil, fmt.Errorf("provider %q is not configured", configured)
	}

	// 2. Single available provider shortcut.
	available := supportingAvailable(capability, providers)
	if len(available) == 1 {
		return available[0], nil
	}

	// 3. Legacy preference walk filtered by availability and capability.
	for _, name := range LegacyPreference {
		for _, p := range providers {
			if p.Name() == name && p.IsAvailable() && Capability(p, capability) {
				return p, nil
			}
		}
	}

	if len(available) > 1 {
		names := make([]string, len(available))
		for i, p := range available {
			names[i] = p.Name()
		}
		return nil, fmt.Errorf("multiple providers available (%s); set search.%s_backend", joinNames(names), capability)
	}
	return nil, nil
}

// SearchErr wraps err in a failed SearchResponse.
func SearchErr(err error) *SearchResponse {
	return &SearchResponse{Success: false, Error: err.Error()}
}

// ExtractErr wraps err in a failed ExtractResponse.
func ExtractErr(err error) *ExtractResponse {
	return &ExtractResponse{Success: false, Error: err.Error()}
}

func supportingAvailable(capability string, providers []Provider) []Provider {
	var out []Provider
	for _, p := range providers {
		if p.IsAvailable() && Capability(p, capability) {
			out = append(out, p)
		}
	}
	return out
}

// SupportingNames lists provider names that support capability.
func SupportingNames(capability string, providers []Provider) []string {
	var out []string
	for _, p := range providers {
		if Capability(p, capability) {
			out = append(out, p.Name())
		}
	}
	return out
}

// JoinNames renders a human-friendly list: "a, b, and c".
func JoinNames(names []string) string {
	return joinNames(names)
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}
