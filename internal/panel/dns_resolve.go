package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	dnsResolveTimeout       = 3 * time.Second
	dnsResolveMaxAddresses  = 64
	dnsResolveMaxConcurrent = 10
)

// netIPResolver is the subset of *net.Resolver used by the DNS helper, so
// tests can inject a stub.
type netIPResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// dnsResolveService backs GET /api/v1/dns/resolve. It only suggests
// candidate addresses to the route editor (for example to pick a
// preferred_ip); nothing it returns is persisted or trusted by the renderer.
type dnsResolveService struct {
	resolver netIPResolver
	timeout  time.Duration
	slots    chan struct{}
	now      func() time.Time
}

type dnsResolveResponse struct {
	Host       string   `json:"host"`
	Addresses  []string `json:"addresses"`
	ResolvedAt string   `json:"resolved_at"`
	Error      string   `json:"error,omitempty"`
}

func newDNSResolveService(resolver netIPResolver) *dnsResolveService {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &dnsResolveService{
		resolver: resolver,
		timeout:  dnsResolveTimeout,
		slots:    make(chan struct{}, dnsResolveMaxConcurrent),
		now:      time.Now,
	}
}

func (s *dnsResolveService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	raw := r.URL.Query().Get("host")
	host, ok := normalizeResolveHost(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_host", "host must be a DNS hostname, not an IP address")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many concurrent DNS lookups")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	addrs, err := s.resolver.LookupNetIP(ctx, "ip", host)
	out := dnsResolveResponse{Host: host, Addresses: sortResolvedAddresses(addrs), ResolvedAt: s.now().UTC().Format(time.RFC3339)}
	if err != nil {
		out.Addresses = []string{}
		out.Error = dnsResolveErrorMessage(ctx, err)
	}
	writeJSON(w, http.StatusOK, out)
}

// normalizeResolveHost applies the route target host rules and additionally
// rejects IP literals: there is nothing to resolve for them.
func normalizeResolveHost(raw string) (string, bool) {
	if containsForbiddenControl(raw) {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "[]") {
		return "", false
	}
	if _, err := netip.ParseAddr(strings.TrimSuffix(trimmed, ".")); err == nil {
		return "", false
	}
	host, ok := normalizeTargetHost(trimmed)
	if !ok || net.ParseIP(host) != nil {
		return "", false
	}
	return host, true
}

// sortResolvedAddresses returns IPv4 addresses first, then IPv6, each group
// sorted and deduplicated, capped at dnsResolveMaxAddresses.
func sortResolvedAddresses(addrs []netip.Addr) []string {
	seen := make(map[netip.Addr]struct{}, len(addrs))
	var v4, v6 []netip.Addr
	for _, addr := range addrs {
		if !addr.IsValid() {
			continue
		}
		addr = addr.Unmap().WithZone("")
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		if addr.Is4() {
			v4 = append(v4, addr)
		} else {
			v6 = append(v6, addr)
		}
	}
	sort.Slice(v4, func(i, j int) bool { return v4[i].Less(v4[j]) })
	sort.Slice(v6, func(i, j int) bool { return v6[i].Less(v6[j]) })
	out := make([]string, 0, len(v4)+len(v6))
	for _, addr := range append(v4, v6...) {
		if len(out) == dnsResolveMaxAddresses {
			break
		}
		out = append(out, addr.String())
	}
	return out
}

func dnsResolveErrorMessage(ctx context.Context, err error) string {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "no such host"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &dnsErr) && dnsErr.IsTimeout):
		return "lookup timed out"
	case errors.Is(err, context.Canceled):
		return "lookup canceled"
	default:
		return "lookup failed"
	}
}
