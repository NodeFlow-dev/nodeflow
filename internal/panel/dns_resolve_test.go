package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubResolver struct {
	mu      sync.Mutex
	addrs   []netip.Addr
	err     error
	block   chan struct{}
	started chan struct{}
	hosts   []string
}

func (s *stubResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	s.mu.Lock()
	s.hosts = append(s.hosts, host)
	s.mu.Unlock()
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.addrs, s.err
}

func dnsHandler(resolver netIPResolver) http.Handler {
	return NewHandler(&fakeStore{}, Config{AdminToken: testAdminToken, dnsResolver: resolver})
}

func decodeResolve(t *testing.T, w *httptest.ResponseRecorder) dnsResolveResponse {
	t.Helper()
	var out dnsResolveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	_, err := time.Parse(time.RFC3339, out.ResolvedAt)
	require.NoError(t, err, out.ResolvedAt)
	return out
}

func TestDNSResolveRequiresAdmin(t *testing.T) {
	stub := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("192.0.2.1")}}
	w := request(t, dnsHandler(stub), http.MethodGet, "/api/v1/dns/resolve?host=example.com", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, stub.hosts)
}

func TestDNSResolveSortsDedupesAndOrdersFamilies(t *testing.T) {
	stub := &stubResolver{addrs: []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("::ffff:192.0.2.9"),
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("192.0.2.9"),
		netip.MustParseAddr("2001:db8::0:1"),
		netip.MustParseAddr("198.51.100.1"),
	}}
	w := request(t, dnsHandler(stub), http.MethodGet, "/api/v1/dns/resolve?host=Example.COM.", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	out := decodeResolve(t, w)
	assert.Equal(t, "example.com", out.Host)
	assert.Equal(t, []string{"192.0.2.9", "192.0.2.10", "198.51.100.1", "2001:db8::1"}, out.Addresses)
	assert.Empty(t, out.Error)
	assert.NotContains(t, w.Body.String(), `"error"`)
	assert.Equal(t, []string{"example.com"}, stub.hosts)
}

func TestDNSResolveCapsAddresses(t *testing.T) {
	var addrs []netip.Addr
	for i := 0; i < 100; i++ {
		addrs = append(addrs, netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", i)))
		addrs = append(addrs, netip.MustParseAddr(fmt.Sprintf("10.0.0.%d", i)))
	}
	out := decodeResolve(t, request(t, dnsHandler(&stubResolver{addrs: addrs}), http.MethodGet, "/api/v1/dns/resolve?host=big.example.com", "", testAdminToken))
	require.Len(t, out.Addresses, 64)
	assert.Equal(t, "10.0.0.0", out.Addresses[0])
	assert.Equal(t, "10.0.0.63", out.Addresses[63])
}

func TestDNSResolveRejectsInvalidHosts(t *testing.T) {
	stub := &stubResolver{}
	h := dnsHandler(stub)
	for _, host := range []string{"", "192.0.2.1", "2001:db8::1", "%5B2001:db8::1%5D", "bad_host.example.com", "a..b", "-a.example.com", "exa%20mple.com", "a.example.com/x", "a.example.com:443", "%0Aevil.com"} {
		w := request(t, h, http.MethodGet, "/api/v1/dns/resolve?host="+host, "", testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, host)
		assert.Contains(t, w.Body.String(), `"invalid_host"`, host)
	}
	assert.Empty(t, stub.hosts)
}

func TestDNSResolveMethodNotAllowed(t *testing.T) {
	w := request(t, dnsHandler(&stubResolver{}), http.MethodPost, "/api/v1/dns/resolve?host=example.com", "", testAdminToken)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestDNSResolveNXDomainIsOKWithError(t *testing.T) {
	stub := &stubResolver{err: &net.DNSError{Err: "no such host", Name: "missing.example.com", IsNotFound: true}}
	w := request(t, dnsHandler(stub), http.MethodGet, "/api/v1/dns/resolve?host=missing.example.com", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code)
	out := decodeResolve(t, w)
	assert.Equal(t, "missing.example.com", out.Host)
	assert.Equal(t, "no such host", out.Error)
	assert.Contains(t, w.Body.String(), `"addresses":[]`)
}

func TestDNSResolveTimeoutIsOKWithError(t *testing.T) {
	svc := newDNSResolveService(&stubResolver{block: make(chan struct{})})
	svc.timeout = 20 * time.Millisecond
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/dns/resolve?host=slow.example.com", nil))
	require.Equal(t, http.StatusOK, w.Code)
	out := decodeResolve(t, w)
	assert.Equal(t, "lookup timed out", out.Error)
	assert.Empty(t, out.Addresses)
	assert.NotNil(t, out.Addresses)
}

func TestDNSResolveLimitsConcurrency(t *testing.T) {
	stub := &stubResolver{block: make(chan struct{}), started: make(chan struct{}, dnsResolveMaxConcurrent)}
	svc := newDNSResolveService(stub)
	var wg sync.WaitGroup
	for i := 0; i < dnsResolveMaxConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/dns/resolve?host=a.example.com", nil))
		}()
	}
	for i := 0; i < dnsResolveMaxConcurrent; i++ {
		<-stub.started
	}
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/dns/resolve?host=a.example.com", nil))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	close(stub.block)
	wg.Wait()
}
