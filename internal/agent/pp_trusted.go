package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// PROXY protocol trusted-source hostnames.
//
// accept_proxy_from may list hostnames: each one stands for every A/AAAA
// address of the name. For a listener with hostnames the Panel renders
//
//	# nf-pp-trusted file=/etc/haproxy/nodeflow/pp-trusted-<frontend>.acl domains=a.example,b.example
//	acl nf_pp_trusted src -f /etc/haproxy/nodeflow/pp-trusted-<frontend>.acl
//	tcp-request connection expect-proxy layer4 if { src <static> } || nf_pp_trusted
//
// The Agent owns the ACL file. Before every "haproxy -c" (validation, apply
// and rollback) it makes sure each annotated file exists, resolving new
// hostnames synchronously, so HAProxy loads the current addresses on reload.
// Afterwards PPTrustedController re-resolves every PPTrustedInterval and,
// only when the address set changed, rewrites the file atomically and swaps
// the loaded ACL through the runtime API (prepare acl / add acl @ver /
// commit acl @ver) without a reload. An unchanged set costs no socket
// traffic. A failed lookup keeps the last known addresses of that name;
// the set is never wiped by a transient DNS failure.

const (
	ppTrustedAnnotation = "# nf-pp-trusted "
	// DefaultPPTrustedDir is the only directory the Agent writes ACL files to.
	DefaultPPTrustedDir = "/etc/haproxy/nodeflow"
	// PPTrustedInterval is the re-resolution period. The Go resolver does not
	// expose record TTLs, so a fixed 30 s period (the documented minimum) is
	// used for every name.
	PPTrustedInterval = 30 * time.Second
	// maxPPTrustedDomains mirrors the Panel limit (32 per route) with room
	// for the union of several routes on one listener.
	maxPPTrustedDomains = 256
	// maxPPTrustedAddresses bounds the addresses written for one file.
	maxPPTrustedAddresses  = 4096
	ppTrustedLookupTimeout = 5 * time.Second
)

var ppTrustedFileName = regexp.MustCompile(`^pp-trusted-[a-z0-9_]{1,160}\.acl$`)

// PPTrustedSet is one annotated ACL file with the hostnames feeding it.
type PPTrustedSet struct {
	File    string
	Domains []string
}

// ParsePPTrustedAnnotations extracts the "# nf-pp-trusted" annotations. Files
// outside dir or with an unexpected name are rejected.
func ParsePPTrustedAnnotations(config []byte, dir string) ([]PPTrustedSet, error) {
	if dir == "" {
		dir = DefaultPPTrustedDir
	}
	var out []PPTrustedSet
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(config))
	scanner.Buffer(make([]byte, 64*1024), MaxManagedConfigBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		rest, ok := strings.CutPrefix(line, ppTrustedAnnotation)
		if !ok {
			continue
		}
		fields, err := annotationFields(rest)
		if err != nil {
			return nil, err
		}
		file := fields["file"]
		if filepath.Dir(file) != filepath.Clean(dir) || !ppTrustedFileName.MatchString(filepath.Base(file)) {
			return nil, fmt.Errorf("nf-pp-trusted file %q is not an allowed ACL path", file)
		}
		if seen[file] {
			return nil, fmt.Errorf("duplicate nf-pp-trusted file %q", file)
		}
		seen[file] = true
		var domains []string
		for _, domain := range strings.Split(fields["domains"], ",") {
			if domain == "" {
				continue
			}
			if !validPPTrustedDomain(domain) {
				return nil, fmt.Errorf("nf-pp-trusted domain %q is invalid", domain)
			}
			domains = append(domains, domain)
		}
		if len(domains) == 0 || len(domains) > maxPPTrustedDomains {
			return nil, fmt.Errorf("nf-pp-trusted %s must list 1..%d domains", file, maxPPTrustedDomains)
		}
		sort.Strings(domains)
		out = append(out, PPTrustedSet{File: file, Domains: domains})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validPPTrustedDomain(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-') {
				return false
			}
		}
	}
	return true
}

// ppTrustedResolver is the subset of *net.Resolver used here.
type ppTrustedResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ppTrustedRuntime is the HAProxy runtime API used to swap a loaded ACL.
type ppTrustedRuntime interface {
	ShowACL(ctx context.Context, ref string) ([]string, bool, error)
	ReplaceACL(ctx context.Context, ref string, values []string) error
}

// PPTrustedController resolves accept_proxy_from hostnames and keeps the
// HAProxy ACL files and loaded ACLs current.
type PPTrustedController struct {
	Manager  *ConfigManager
	Runtime  ppTrustedRuntime
	Resolver ppTrustedResolver
	Dir      string
	Logger   *log.Logger

	mu sync.Mutex
	// known is the last successful lookup per hostname.
	known map[string][]string
	// failing marks hostnames whose last lookup failed (for log-on-change).
	failing map[string]bool
	// applied is the address set the loaded ACL holds per file, valid while
	// appliedEpoch equals the ConfigManager mutation epoch.
	applied      map[string]string
	appliedEpoch uint64
	lastSum      [32]byte
}

// NewPPTrustedController wires the controller into manager: every HAProxy
// validation first materialises the annotated ACL files.
func NewPPTrustedController(manager *ConfigManager, runtime ppTrustedRuntime, resolver ppTrustedResolver) *PPTrustedController {
	c := &PPTrustedController{Manager: manager, Runtime: runtime, Resolver: resolver, Dir: DefaultPPTrustedDir}
	if manager != nil {
		manager.PrepareConfig = c.Prepare
	}
	return c
}

func (c *PPTrustedController) dir() string {
	if c.Dir == "" {
		return DefaultPPTrustedDir
	}
	return c.Dir
}

func (c *PPTrustedController) logger() *log.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return log.Default()
}

// Prepare is called under the ConfigManager lock before "haproxy -c". It
// writes every annotated ACL file whose content is missing or stale for the
// candidate's hostnames. Lookups that fail fall back to the last known
// addresses, then to the current file content, then to an empty file (nobody
// is trusted through the hostname until it resolves).
func (c *PPTrustedController) Prepare(ctx context.Context, config []byte) error {
	sets, err := ParsePPTrustedAnnotations(config, c.dir())
	if err != nil || len(sets) == 0 {
		return err
	}
	if err := os.MkdirAll(c.dir(), 0755); err != nil {
		return fmt.Errorf("create %s: %w", c.dir(), err)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, ppTrustedLookupTimeout)
	defer cancel()
	for _, set := range sets {
		current, _ := readPPTrustedFile(set.File)
		desired := c.desired(lookupCtx, set, current)
		if _, statErr := os.Stat(set.File); statErr == nil && strings.Join(current, "\n") == strings.Join(desired, "\n") {
			continue
		}
		if err := writePPTrustedFile(set.File, desired); err != nil {
			return fmt.Errorf("write %s: %w", set.File, err)
		}
	}
	return nil
}

// desired resolves every hostname of set and returns the sorted address
// union. A failed name contributes its last known addresses; when it has
// none (fresh Agent start) the current file content is kept as a whole.
func (c *PPTrustedController) desired(ctx context.Context, set PPTrustedSet, current []string) []string {
	union := make(map[string]struct{})
	keepCurrent := false
	for _, domain := range set.Domains {
		addrs, ok := c.lookup(ctx, domain)
		if !ok {
			keepCurrent = true
		}
		for _, addr := range addrs {
			union[addr] = struct{}{}
		}
	}
	if keepCurrent {
		for _, addr := range current {
			union[addr] = struct{}{}
		}
	}
	out := make([]string, 0, len(union))
	for addr := range union {
		out = append(out, addr)
	}
	sort.Strings(out)
	if len(out) > maxPPTrustedAddresses {
		out = out[:maxPPTrustedAddresses]
	}
	return out
}

// lookup returns the addresses of domain and whether they are backed by a
// successful lookup (fresh or remembered).
func (c *PPTrustedController) lookup(ctx context.Context, domain string) ([]string, bool) {
	var addrs []netip.Addr
	var err error
	if c.Resolver != nil {
		addrs, err = c.Resolver.LookupNetIP(ctx, "ip", domain)
	} else {
		err = errors.New("resolver is not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known == nil {
		c.known = make(map[string][]string)
		c.failing = make(map[string]bool)
	}
	if err == nil && len(addrs) > 0 {
		seen := make(map[string]struct{}, len(addrs))
		out := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			text := addr.Unmap().WithZone("").String()
			if _, dup := seen[text]; !dup {
				seen[text] = struct{}{}
				out = append(out, text)
			}
		}
		sort.Strings(out)
		c.known[domain] = out
		if c.failing[domain] {
			c.logger().Printf("pp-trusted: %s resolves again (%d address(es))", domain, len(out))
			delete(c.failing, domain)
		}
		return out, true
	}
	if !c.failing[domain] {
		msg := "no addresses"
		if err != nil {
			msg = err.Error()
		}
		c.logger().Printf("pp-trusted: cannot resolve %s, keeping last known addresses: %s", domain, msg)
		c.failing[domain] = true
	}
	previous, ok := c.known[domain]
	return previous, ok
}

// Reconcile runs one pass over the active managed config and returns the
// files whose loaded ACL was replaced.
func (c *PPTrustedController) Reconcile(ctx context.Context) ([]string, error) {
	if c == nil || c.Manager == nil || c.Runtime == nil {
		return nil, errors.New("pp-trusted controller is not configured")
	}
	config, _, err := c.Manager.managedConfigSnapshot()
	if err != nil {
		return nil, fmt.Errorf("read managed config: %w", err)
	}
	sets, err := ParsePPTrustedAnnotations(config, c.dir())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(config)
	configChanged := sum != c.lastSum
	if len(sets) == 0 {
		return nil, c.Manager.RunSerialized(func() error { return c.cleanup(nil) })
	}
	// Resolve outside the ConfigManager lock: DNS may be slow.
	lookupCtx, cancel := context.WithTimeout(ctx, ppTrustedLookupTimeout)
	defer cancel()
	type plan struct {
		set     PPTrustedSet
		desired []string
	}
	plans := make([]plan, 0, len(sets))
	for _, set := range sets {
		current, _ := readPPTrustedFile(set.File)
		plans = append(plans, plan{set: set, desired: c.desired(lookupCtx, set, current)})
	}
	syncFile := func(p plan, want string) error {
		// A validate-only candidate may have rewritten the file; keep it
		// equal to the active set so an external reload loads the same.
		current, readErr := readPPTrustedFile(p.set.File)
		if readErr == nil && strings.Join(current, "\n") == want {
			return nil
		}
		return writePPTrustedFile(p.set.File, p.desired)
	}
	var replaced []string
	err = c.Manager.RunSerialized(func() error {
		live, readErr := os.ReadFile(c.Manager.ManagedConfig)
		if readErr != nil || sha256.Sum256(live) != sum {
			return nil // config changed meanwhile; the next pass handles it
		}
		epoch := c.Manager.MutationEpoch()
		if c.applied == nil || epoch != c.appliedEpoch || configChanged {
			// A reload may have loaded different file content: forget what
			// the runtime is believed to hold and re-read it once.
			c.applied = make(map[string]string)
			c.appliedEpoch = epoch
		}
		for _, p := range plans {
			want := strings.Join(p.desired, "\n")
			loaded, known := c.applied[p.set.File]
			if !known {
				values, present, showErr := c.Runtime.ShowACL(ctx, p.set.File)
				if showErr != nil {
					return fmt.Errorf("show acl %s: %w", p.set.File, showErr)
				}
				if !present {
					// Not loaded by the running HAProxy (not started yet):
					// keep the file current for its next start.
					if syncErr := syncFile(p, want); syncErr != nil {
						return fmt.Errorf("write %s: %w", p.set.File, syncErr)
					}
					continue
				}
				sort.Strings(values)
				loaded = strings.Join(values, "\n")
				c.applied[p.set.File] = loaded
			}
			if loaded == want {
				if syncErr := syncFile(p, want); syncErr != nil {
					return fmt.Errorf("write %s: %w", p.set.File, syncErr)
				}
				continue
			}
			if writeErr := syncFile(p, want); writeErr != nil {
				return fmt.Errorf("write %s: %w", p.set.File, writeErr)
			}
			if replaceErr := c.Runtime.ReplaceACL(ctx, p.set.File, p.desired); replaceErr != nil {
				delete(c.applied, p.set.File)
				return fmt.Errorf("replace acl %s: %w", p.set.File, replaceErr)
			}
			c.applied[p.set.File] = want
			replaced = append(replaced, p.set.File)
			c.logger().Printf("pp-trusted: %s now trusts %d address(es) from %s", filepath.Base(p.set.File), len(p.desired), strings.Join(p.set.Domains, ","))
		}
		// Files of a removed listener or of a validate-only candidate are
		// dropped; a directory listing costs no HAProxy traffic.
		keep := make(map[string]bool, len(sets))
		for _, set := range sets {
			keep[set.File] = true
		}
		if cleanupErr := c.cleanup(keep); cleanupErr != nil {
			return cleanupErr
		}
		c.lastSum = sum
		return nil
	})
	return replaced, err
}

// cleanup removes ACL files that the active config no longer references.
// Called under the ConfigManager lock, so it never races a validation.
func (c *PPTrustedController) cleanup(keep map[string]bool) error {
	entries, err := os.ReadDir(c.dir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(c.dir(), entry.Name())
		if entry.Type().IsRegular() && ppTrustedFileName.MatchString(entry.Name()) && !keep[path] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// Run reconciles every interval until ctx is cancelled. Errors are logged
// once per distinct message.
func (c *PPTrustedController) Run(ctx context.Context, interval time.Duration) {
	if c == nil {
		return
	}
	if interval <= 0 {
		interval = PPTrustedInterval
	}
	lastErr := ""
	tick := func() {
		runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := c.Reconcile(runCtx)
		cancel()
		if err != nil {
			if msg := err.Error(); msg != lastErr && ctx.Err() == nil {
				c.logger().Printf("pp-trusted reconciliation failed: %s", msg)
				lastErr = msg
			}
			return
		}
		if lastErr != "" {
			c.logger().Printf("pp-trusted reconciliation recovered")
			lastErr = ""
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

func readPPTrustedFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if addr, parseErr := netip.ParseAddr(line); parseErr == nil {
			out = append(out, addr.Unmap().String())
		}
	}
	sort.Strings(out)
	return out, nil
}

func writePPTrustedFile(path string, addrs []string) error {
	var b strings.Builder
	for _, addr := range addrs {
		b.WriteString(addr)
		b.WriteByte('\n')
	}
	return atomicWrite(path, []byte(b.String()), 0644)
}
