package webtools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/infa/task_relay/master/agent/policy"
)

// Deps wires the web tools' collaborators.
type Deps struct {
	Audit                *policy.AuditLogger
	Paths                policy.PathEvaluator
	DomainAllowList      []string // suffix match; empty = all allowed
	DomainDenyList       []string // suffix match, always wins
	AllowPrivateNetworks bool
	MaxBytes             int64
	Timeout              time.Duration
	Session              string
}

// validateURL parses raw and enforces scheme and domain policy.
func (d *Deps) validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q: only http/https allowed", u.Scheme)
	}
	if err := d.checkDomain(u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// checkDomain applies the deny list (always wins) then the allow list.
func (d *Deps) checkDomain(host string) error {
	host = strings.ToLower(host)
	for _, suffix := range d.DomainDenyList {
		if domainSuffixMatch(host, suffix) {
			return fmt.Errorf("domain %q denied by policy", host)
		}
	}
	if len(d.DomainAllowList) == 0 {
		return nil
	}
	for _, suffix := range d.DomainAllowList {
		if domainSuffixMatch(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("domain %q not in allow list", host)
}

func domainSuffixMatch(host, suffix string) bool {
	suffix = strings.ToLower(strings.TrimPrefix(suffix, "."))
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

// isPrivateIP reports whether ip is loopback, private, link-local,
// unspecified, CGNAT, or ULA. v4-mapped v6 addresses are unmapped first.
func isPrivateIP(ip netip.Addr) bool {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	ula := netip.MustParsePrefix("fc00::/7")
	return cgnat.Contains(ip) || ula.Contains(ip)
}

// secureTransport returns a transport that blocks dialing private IPs
// unless allowPrivate is set. The per-IP check happens at dial time so
// redirects to public domains resolving to private IPs are still blocked.
func secureTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	transport := &http.Transport{
		// Proxies are intentionally unsupported: the SSRF guard validates
		// the dial target, and a proxy would make the guard validate the
		// proxy address instead of the target. An enterprise egress proxy
		// can be added later as an explicit, audited config knob.
		Proxy:                 nil,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("split addr: %w", err)
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %q: no addresses", host)
		}
		if !allowPrivate {
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("dial to private address %s denied by policy", ip)
				}
			}
		}
		// Dial the validated IP to close the DNS-rebinding window between
		// validation and connect. TLS ServerName still derives from the
		// request URL host, not from this dial address.
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return transport
}

// resolveIn joins relative paths against root; absolute paths pass through
// (the policy evaluator already validated them). Duplicated from filetools
// to avoid a webtools→filetools dependency.
func resolveIn(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// auditDenied records a policy denial.
func (d *Deps) auditDenied(op, target, decision string) error {
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   target,
		Decision:  decision,
		ExitCode:  -1,
		Session:   d.Session,
	}
	if err := d.Audit.Log(entry); err != nil {
		return fmt.Errorf("%s denied and audit failed: %w", op, err)
	}
	return nil
}

// auditOp records a completed operation. Stdout carries the content for hashing.
func (d *Deps) auditOp(op, target, content string, exitCode int, err error) error {
	entry := policy.AuditEntry{
		Operation: op,
		JobID:     uuid.NewString(),
		Command:   target,
		Backend:   "local",
		Decision:  policy.Allow.String(),
		ExitCode:  exitCode,
		Stdout:    content,
		Session:   d.Session,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if logErr := d.Audit.Log(entry); logErr != nil {
		return fmt.Errorf("audit failed: %w", logErr)
	}
	return nil
}
