package search

import (
	"fmt"

	"github.com/go-resty/resty/v2"
)

// Base is the shared provider scaffold: name, endpoint, credentials, resty
// client and capability flags. Providers embed it and only implement the
// wire protocol in Search/Extract.
type Base struct {
	NameVal    string
	BaseURL    string
	APIKey     string
	SearchCap  bool
	ExtractCap bool
	Client     *resty.Client
}

// BaseOpts configures NewBase.
type BaseOpts struct {
	// DefaultBaseURL is used when the provider config has no base_url
	// (e.g. hosted APIs). Empty means base_url is mandatory.
	DefaultBaseURL string
	Search         bool
	Extract        bool
}

// NewBase builds a provider scaffold from config, or returns false when the
// provider block is disabled or not meaningfully configured.
func NewBase(name string, cfg *Config, opts BaseOpts) (*Base, bool) {
	pc := ProviderConfigFor(cfg, name)
	if !ProviderEnabled(name, pc) {
		return nil, false
	}
	if pc.BaseURL == "" && opts.DefaultBaseURL == "" {
		return nil, false
	}
	// A default base URL must not activate a provider the user never
	// configured: require either an explicit base_url or an enabled flag.
	if pc.BaseURL == "" && opts.DefaultBaseURL != "" && pc.APIKey == "" && pc.Enabled == nil {
		return nil, false
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = opts.DefaultBaseURL
	}
	client := resty.New().SetTimeout(ProviderTimeout(cfg, pc))
	return &Base{
		NameVal:    name,
		BaseURL:    baseURL,
		APIKey:     pc.APIKey,
		SearchCap:  opts.Search,
		ExtractCap: opts.Extract,
		Client:     client,
	}, true
}

func (b *Base) Name() string          { return b.NameVal }
func (b *Base) SupportsSearch() bool  { return b.SearchCap }
func (b *Base) SupportsExtract() bool { return b.ExtractCap }

// IsAvailable reports whether credentials are present. Providers that need no
// API key (searxng, ddgs) only require a base URL.
func (b *Base) IsAvailable() bool {
	if b.BaseURL == "" {
		return false
	}
	if !RequiresAPIKey(b.NameVal) {
		return true
	}
	return b.APIKey != ""
}

// ErrorFor renders a provider-scoped HTTP error.
func (b *Base) ErrorFor(status int, body string) error {
	return fmt.Errorf("%s api status %d: %s", b.NameVal, status, truncateRunes(body, 512))
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
