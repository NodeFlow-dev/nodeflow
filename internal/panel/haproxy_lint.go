package panel

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Static HAProxy configuration linter for the advanced editor. The Panel
// image has no haproxy binary, so this is a best-effort structural check;
// the Agent still runs `haproxy -c` before every apply and stays the final
// gate. Errors are things HAProxy is known to reject; warnings are things it
// may accept but that are probably mistakes (or keywords this allowlist does
// not know yet).

const (
	LintSeverityError   = "error"
	LintSeverityWarning = "warning"

	maxLintIssues              = 500
	maxLintLineWords           = 64 // HAProxy MAX_LINE_ARGS
	maxServerTemplateExpansion = 4096
)

type LintIssue struct {
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndColumn int    `json:"end_column"`
	Severity  string `json:"severity"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type LintSection struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

type LintResult struct {
	Valid            bool          `json:"valid"`
	Issues           []LintIssue   `json:"issues"`
	ListenerTCPPorts []int         `json:"listener_tcp_ports"`
	Sections         []LintSection `json:"sections"`
	// listenerPortsOverflow is set when bind lines expand to more ports than
	// the firewall plan accepts; such a list must not be stored.
	listenerPortsOverflow bool
}

// Counts returns the number of error and warning issues.
func (r LintResult) Counts() (errs, warnings int) {
	for _, issue := range r.Issues {
		if issue.Severity == LintSeverityError {
			errs++
		} else {
			warnings++
		}
	}
	return errs, warnings
}

// FirewallPortsUsable reports whether ListenerTCPPorts may be stored as a
// complete firewall listener plan.
func (r LintResult) FirewallPortsUsable() bool {
	return !r.listenerPortsOverflow && len(r.ListenerTCPPorts) <= maxFirewallPorts
}

var lintTimeoutValue = regexp.MustCompile(`^[0-9]+(us|ms|s|m|h|d)?$`)
var lintProxyName = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

var lintSectionKeywords = map[string]bool{
	"global": true, "defaults": true, "frontend": true, "backend": true, "listen": true,
	"resolvers": true, "peers": true, "userlist": true, "cache": true, "program": true,
	"http-errors": true, "ring": true, "log-forward": true, "mailers": true,
	"crt-store": true, "traces": true, "acme": true,
}

// Address prefixes accepted by HAProxy's str2sa_range. Non-network families
// carry no port.
var lintAddrFamilies = map[string]string{
	"ipv4": "tcp", "ipv6": "tcp", "tcp": "tcp", "tcp4": "tcp", "tcp6": "tcp",
	"mptcp": "tcp", "mptcp4": "tcp", "mptcp6": "tcp",
	"udp": "udp", "udp4": "udp", "udp6": "udp", "quic": "udp", "quic4": "udp", "quic6": "udp",
	"unix": "local", "abns": "local", "abnsz": "local", "fd": "local", "sockpair": "local",
	"rhttp": "local", "uxst": "local", "uxdg": "local",
}

type lintWord struct {
	text   string
	col    int  // 1-based rune column
	end    int  // exclusive
	quoted bool // contained quotes or backslash escapes
}

type lintProxy struct {
	typ     string
	name    string
	line    int
	servers map[string]bool
}

type lintBackendRef struct {
	name string
	line int
	word lintWord
}

type lintCond struct {
	line    int
	sawElse bool
}

type linter struct {
	issues        []LintIssue
	truncated     bool
	sections      []LintSection
	frontends     map[string]int
	backends      map[string]*lintProxy
	namedSections map[string]map[string]int
	refs          []lintBackendRef
	ports         map[int]struct{}
	portOverflow  bool
	namedDefaults bool
}

func (l *linter) add(line int, w lintWord, severity, code, message string) {
	if len(l.issues) >= maxLintIssues {
		l.truncated = true
		return
	}
	col, end := w.col, w.end
	if col < 1 {
		col = 1
	}
	if end < col {
		end = col
	}
	l.issues = append(l.issues, LintIssue{Line: line, Column: col, EndColumn: end, Severity: severity, Code: code, Message: message})
}

func (l *linter) errorf(line int, w lintWord, code, format string, args ...any) {
	l.add(line, w, LintSeverityError, code, fmt.Sprintf(format, args...))
}

func (l *linter) warnf(line int, w lintWord, code, format string, args ...any) {
	l.add(line, w, LintSeverityWarning, code, fmt.Sprintf(format, args...))
}

// LintHAProxyConfig statically checks a complete HAProxy configuration.
func LintHAProxyConfig(cfg string) LintResult {
	result, _ := lintHAProxy(cfg)
	return result
}

// LintHAProxyConfigForNode additionally warns when runtime names that the
// last generated revision exposed (nf_be_*/nf_srv_*) are missing: traffic
// statistics, quotas and Agent weight control are keyed by them.
func LintHAProxyConfigForNode(cfg string, expected []RouteRuntimeNames) LintResult {
	result, l := lintHAProxy(cfg)
	if l == nil {
		return result
	}
	seen := make(map[string]bool)
	for _, runtime := range expected {
		backend := runtime.Backend
		if !strings.HasPrefix(backend, "nf_be_") || seen[backend] {
			continue
		}
		seen[backend] = true
		proxy := l.backends[backend]
		if proxy == nil {
			l.warnf(1, lintWord{col: 1, end: 2}, "runtime_name_missing",
				"Нет backend «%s» (маршрут %s): трафик и квоты этого маршрута не будут учитываться", backend, runtime.RouteID)
			continue
		}
		if runtime.MultiServer || !strings.HasPrefix(runtime.Server, "nf_srv_") {
			continue
		}
		if !proxy.servers[runtime.Server] {
			l.warnf(proxy.line, lintWord{col: 1, end: 1 + utf8.RuneCountInString(proxy.typ+" "+proxy.name)}, "runtime_name_missing",
				"В backend «%s» нет server «%s»: квоты маршрута не будут применяться", backend, runtime.Server)
		}
	}
	return l.finish()
}

func lintHAProxy(cfg string) (LintResult, *linter) {
	l := &linter{
		frontends:     map[string]int{},
		backends:      map[string]*lintProxy{},
		namedSections: map[string]map[string]int{},
		ports:         map[int]struct{}{},
	}
	if len(cfg) > MaxManagedConfigBytes {
		l.errorf(1, lintWord{col: 1, end: 1}, "config_too_large", "Конфигурация больше %d КиБ", MaxManagedConfigBytes/1024)
		return l.finish(), nil
	}
	if strings.TrimSpace(cfg) == "" {
		l.errorf(1, lintWord{col: 1, end: 1}, "config_empty", "Конфигурация пуста")
		return l.finish(), nil
	}
	var (
		sectionType string
		proxy       *lintProxy
		conds       []lintCond
	)
	lines := strings.Split(cfg, "\n")
	for index, raw := range lines {
		lineNo := index + 1
		if i := strings.IndexByte(raw, 0); i >= 0 {
			col := utf8.RuneCountInString(raw[:i]) + 1
			l.errorf(lineNo, lintWord{col: col, end: col + 1}, "nul_byte", "Недопустимый NUL-байт")
			continue
		}
		if !utf8.ValidString(raw) {
			l.errorf(lineNo, lintWord{col: 1, end: 1 + utf8.RuneCountInString(raw)}, "invalid_utf8", "Строка не в кодировке UTF-8")
			continue
		}
		words, unterminated := lintTokenize(raw)
		if unterminated > 0 {
			l.errorf(lineNo, lintWord{col: unterminated, end: unterminated + 1}, "unterminated_quote", "Незакрытая кавычка")
		}
		if len(words) == 0 {
			continue
		}
		if len(words) > maxLintLineWords {
			l.errorf(lineNo, words[maxLintLineWords], "too_many_words", "Больше %d слов в строке: HAProxy отбросит остаток", maxLintLineWords)
		}
		kw := words[0]
		// Conditional blocks.
		if strings.HasPrefix(kw.text, ".") {
			switch kw.text {
			case ".if":
				if len(words) < 2 {
					l.errorf(lineNo, kw, "cond_missing_expression", "У .if нет условия")
				}
				conds = append(conds, lintCond{line: lineNo})
			case ".elif":
				if len(conds) == 0 {
					l.errorf(lineNo, kw, "cond_unbalanced", ".elif без .if")
				} else if conds[len(conds)-1].sawElse {
					l.errorf(lineNo, kw, "cond_unbalanced", ".elif после .else")
				}
			case ".else":
				if len(conds) == 0 {
					l.errorf(lineNo, kw, "cond_unbalanced", ".else без .if")
				} else if conds[len(conds)-1].sawElse {
					l.errorf(lineNo, kw, "cond_unbalanced", "Повторный .else")
				} else {
					conds[len(conds)-1].sawElse = true
				}
			case ".endif":
				if len(conds) == 0 {
					l.errorf(lineNo, kw, "cond_unbalanced", ".endif без .if")
				} else {
					conds = conds[:len(conds)-1]
				}
			case ".diag", ".notice", ".warning", ".alert", ".line":
			default:
				l.warnf(lineNo, kw, "unknown_directive", "Неизвестная директива «%s»", kw.text)
			}
			continue
		}
		if lintSectionKeywords[kw.text] {
			sectionType = kw.text
			proxy = l.openSection(lineNo, words)
			continue
		}
		if sectionType == "" {
			if kw.col == 1 && !lintKnownKeyword("global", kw.text) && !lintKnownKeyword("defaults", kw.text) {
				l.errorf(lineNo, kw, "unknown_section", "Неизвестная секция «%s»", kw.text)
			} else {
				l.errorf(lineNo, kw, "outside_section", "Директива «%s» вне секции", kw.text)
			}
			continue
		}
		// "no"/"default" modifiers shift the arguments (cfgparse.c).
		modifier := ""
		if (kw.text == "no" || kw.text == "default") && len(words) > 1 {
			modifier = kw.text
			words = words[1:]
			kw = words[0]
		}
		// HAProxy ignores indentation; an unknown keyword at column 1 most
		// likely is a misspelled section header.
		known := lintKnownKeyword(sectionType, kw.text)
		if kw.col == 1 && !known && modifier == "" {
			if base, n := lintTrailingPunct(kw.text); n > 0 && lintKnownKeyword(sectionType, base) {
				l.errorf(lineNo, lintPunctWord(kw, n), "stray_punctuation", "Лишний символ «%s» после «%s»", kw.text[len(base):], base)
			} else {
				l.errorf(lineNo, kw, "unknown_section", "Неизвестная секция «%s»", kw.text)
			}
			continue
		}
		if !known {
			l.unknownDirective(lineNo, sectionType, kw)
			continue
		}
		if modifier != "" && !lintNegatable(sectionType, kw.text) {
			l.errorf(lineNo, kw, "negation_not_supported", "Префикс «%s» не поддерживается для «%s»", modifier, kw.text)
			continue
		}
		if lintIsProxySection(sectionType) && !l.proxyKeywordAllowed(lineNo, sectionType, words) {
			continue
		}
		before := len(l.issues)
		l.directive(lineNo, sectionType, proxy, words, modifier)
		l.strayTail(lineNo, words, before)
	}
	for _, cond := range conds {
		l.errorf(cond.line, lintWord{col: 1, end: 4}, "cond_unbalanced", ".if без .endif")
	}
	for _, ref := range l.refs {
		if l.backends[ref.name] == nil {
			l.errorf(ref.line, ref.word, "undefined_backend", "Backend «%s» не определён", ref.name)
		}
	}
	return l.finish(), l
}

func (l *linter) finish() LintResult {
	issues := append([]LintIssue(nil), l.issues...)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Line != issues[j].Line {
			return issues[i].Line < issues[j].Line
		}
		return issues[i].Column < issues[j].Column
	})
	if l.truncated {
		issues = append(issues, LintIssue{Line: 1, Column: 1, EndColumn: 1, Severity: LintSeverityWarning, Code: "too_many_issues", Message: fmt.Sprintf("Показаны первые %d замечаний", maxLintIssues)})
	}
	ports := make([]int, 0, len(l.ports))
	for port := range l.ports {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	sections := append([]LintSection{}, l.sections...)
	result := LintResult{Issues: issues, ListenerTCPPorts: ports, Sections: sections, listenerPortsOverflow: l.portOverflow}
	errs, _ := result.Counts()
	result.Valid = errs == 0
	if result.Issues == nil {
		result.Issues = []LintIssue{}
	}
	return result
}

func (l *linter) openSection(lineNo int, words []lintWord) *lintProxy {
	kw := words[0]
	name := ""
	if len(words) > 1 {
		name = words[1].text
	}
	l.sections = append(l.sections, LintSection{Type: kw.text, Name: name, Line: lineNo})
	switch kw.text {
	case "global":
		return nil
	case "defaults":
		// "defaults from <name>" is anonymous; "defaults <name> [from <x>]" is named.
		l.namedDefaults = name != "" && !(name == "from" && len(words) == 3)
		return nil
	case "frontend", "backend", "listen":
		if name == "" {
			l.errorf(lineNo, kw, "missing_name", "У секции %s нет имени", kw.text)
			return &lintProxy{typ: kw.text, line: lineNo, servers: map[string]bool{}}
		}
		nameWord := words[1]
		if !lintProxyName.MatchString(name) {
			l.errorf(lineNo, nameWord, "invalid_name", "Недопустимые символы в имени «%s»", name)
		}
		proxy := &lintProxy{typ: kw.text, name: name, line: lineNo, servers: map[string]bool{}}
		if kw.text != "backend" {
			if prev, dup := l.frontends[name]; dup {
				l.errorf(lineNo, nameWord, "duplicate_proxy", "Прокси «%s» уже объявлен в строке %d", name, prev)
			} else {
				l.frontends[name] = lineNo
			}
		}
		if kw.text != "frontend" {
			if prev, dup := l.backends[name]; dup {
				if kw.text == "backend" || l.frontends[name] == lineNo {
					l.errorf(lineNo, nameWord, "duplicate_proxy", "Прокси «%s» уже объявлен в строке %d", name, prev.line)
				}
			} else {
				l.backends[name] = proxy
			}
		}
		if kw.text == "listen" && len(words) > 2 {
			// Legacy "listen <name> <addr>" syntax.
			l.bindAddresses(lineNo, words[2], true)
		}
		return proxy
	default:
		if name != "" {
			seen := l.namedSections[kw.text]
			if seen == nil {
				seen = map[string]int{}
				l.namedSections[kw.text] = seen
			}
			if prev, dup := seen[name]; dup {
				l.errorf(lineNo, words[1], "duplicate_section", "Секция %s «%s» уже объявлена в строке %d", kw.text, name, prev)
			} else {
				seen[name] = lineNo
			}
		} else if kw.text != "traces" && kw.text != "crt-store" && kw.text != "program" {
			l.errorf(lineNo, kw, "missing_name", "У секции %s нет имени", kw.text)
		}
		if kw.text == "peers" {
			return &lintProxy{typ: kw.text, name: name, line: lineNo, servers: map[string]bool{}}
		}
		return nil
	}
}

func (l *linter) directive(lineNo int, section string, proxy *lintProxy, words []lintWord, modifier string) {
	kw := words[0]
	proxySection := lintIsProxySection(section)
	switch kw.text {
	case "timeout":
		l.timeout(lineNo, section, words)
	case "option":
		if proxySection {
			l.option(lineNo, section, words, modifier)
		}
	case "mode":
		if proxySection {
			l.mode(lineNo, section, words)
		}
	case "balance":
		if proxySection {
			l.balance(lineNo, words)
		}
	case "hash-type":
		if proxySection {
			l.hashType(lineNo, words)
		}
	case "log":
		if (proxySection || section == "global") && modifier == "" {
			l.logTarget(lineNo, section, words)
		}
	case "stats":
		if section == "global" && len(words) > 2 && words[1].text == "socket" {
			l.bindKeywords(lineNo, words, 3)
		}
	case "default-server":
		l.serverKeywords(lineNo, words, 1)
	case "bind":
		if len(words) < 2 {
			l.errorf(lineNo, kw, "bind_missing_address", "У bind нет адреса")
			return
		}
		l.bindAddresses(lineNo, words[1], section == "frontend" || section == "listen")
		l.bindKeywords(lineNo, words, 2)
	case "server", "server-template":
		switch section {
		case "backend", "listen", "peers", "ring":
		default:
			return
		}
		if kw.text == "server" {
			l.server(lineNo, section, proxy, words)
		} else {
			l.serverTemplate(lineNo, proxy, words)
		}
	case "nameserver", "peer", "mailer":
		if len(words) < 3 {
			l.errorf(lineNo, kw, "missing_address", "У %s нет имени или адреса", kw.text)
			return
		}
		l.checkServerAddress(lineNo, words[2], false)
	case "use_backend", "default_backend":
		if len(words) < 2 {
			l.errorf(lineNo, kw, "missing_backend", "У %s не указан backend", kw.text)
			return
		}
		target := words[1]
		if strings.Contains(target.text, "%[") || strings.ContainsAny(target.text, "{$") {
			return
		}
		l.refs = append(l.refs, lintBackendRef{name: target.text, line: lineNo, word: target})
	}
}

func (l *linter) timeout(lineNo int, section string, words []lintWord) {
	kw := words[0]
	if len(words) < 2 {
		l.errorf(lineNo, kw, "timeout_missing_kind", "У timeout не указан тип")
		return
	}
	class := section
	switch section {
	case "defaults", "frontend", "backend", "listen":
		class = "proxy"
	}
	if kinds := lintKW.timeouts[class]; kinds != nil && !kinds[words[1].text] {
		kind := words[1]
		if base, n := lintTrailingPunct(kind.text); n > 0 && kinds[base] {
			l.errorf(lineNo, lintPunctWord(kind, n), "stray_punctuation", "Лишний символ «%s» после «%s»", kind.text[len(base):], base)
		} else {
			l.errorf(lineNo, kind, "unknown_timeout", "Неизвестный тип timeout «%s»", kind.text)
		}
		return
	}
	if class == "proxy" {
		if flags, ok := lintKW.proxySub["timeout "+words[1].text]; ok {
			if allowed, _ := lintSectionAllowed(flags, section, true); !allowed {
				l.warnf(lineNo, words[1], "timeout_ignored", "timeout %s игнорируется в секции %s", words[1].text, section)
			}
		}
	}
	if len(words) < 3 {
		l.errorf(lineNo, words[1], "timeout_missing_value", "У timeout %s нет значения", words[1].text)
		return
	}
	value := words[2]
	if strings.Contains(value.text, "$") {
		return
	}
	if !lintTimeoutValue.MatchString(value.text) {
		l.errorf(lineNo, value, "invalid_timeout", "Неверное значение timeout «%s» (пример: 5s, 500ms, 15m)", value.text)
	}
}

func (l *linter) server(lineNo int, section string, proxy *lintProxy, words []lintWord) {
	kw := words[0]
	if len(words) < 2 {
		l.errorf(lineNo, kw, "server_missing_name", "У server нет имени")
		return
	}
	name := words[1]
	l.addServerName(lineNo, proxy, name, name.text)
	if len(words) < 3 {
		// A peers section names the local peer without an address.
		if section != "peers" {
			l.errorf(lineNo, name, "server_missing_address", "У server «%s» нет адреса", name.text)
		}
		return
	}
	l.checkServerAddress(lineNo, words[2], true)
	l.serverKeywords(lineNo, words, 3)
}

func (l *linter) serverTemplate(lineNo int, proxy *lintProxy, words []lintWord) {
	kw := words[0]
	if len(words) < 4 {
		l.errorf(lineNo, kw, "server_template_incomplete", "server-template: нужны префикс, количество и адрес")
		return
	}
	prefix, count := words[1], words[2]
	low, high := 1, 0
	if a, b, ok := strings.Cut(count.text, "-"); ok {
		x, errA := strconv.Atoi(a)
		y, errB := strconv.Atoi(b)
		if errA != nil || errB != nil || x < 1 || y < x {
			l.errorf(lineNo, count, "invalid_server_template_count", "Неверный диапазон «%s»", count.text)
			return
		}
		low, high = x, y
	} else {
		n, err := strconv.Atoi(count.text)
		if err != nil || n < 1 {
			l.errorf(lineNo, count, "invalid_server_template_count", "Неверное количество «%s»", count.text)
			return
		}
		high = n
	}
	if high-low < maxServerTemplateExpansion {
		for i := low; i <= high; i++ {
			l.addServerName(lineNo, proxy, prefix, prefix.text+strconv.Itoa(i))
		}
	}
	l.checkServerAddress(lineNo, words[3], true)
	l.serverKeywords(lineNo, words, 4)
}

func (l *linter) addServerName(lineNo int, proxy *lintProxy, w lintWord, name string) {
	if proxy == nil {
		return
	}
	if proxy.servers[name] {
		l.errorf(lineNo, w, "duplicate_server", "Server «%s» уже объявлен в %s «%s»", name, proxy.typ, proxy.name)
		return
	}
	proxy.servers[name] = true
}

// checkServerAddress validates a server/nameserver address: host:port,
// [ipv6]:port, a Unix path or a non-network family prefix.
func (l *linter) checkServerAddress(lineNo int, w lintWord, portOptional bool) {
	addr := w.text
	if strings.Contains(addr, "$") {
		return
	}
	family, rest := lintSplitFamily(addr)
	if family == "local" || strings.HasPrefix(rest, "/") {
		if rest == "" {
			l.errorf(lineNo, w, "invalid_address", "Пустой адрес «%s»", addr)
		}
		return
	}
	host, port, hasPort, msg := lintSplitHostPort(rest)
	if msg != "" {
		l.errorf(lineNo, w, "invalid_address", "%s: «%s»", msg, addr)
		return
	}
	if host == "" {
		l.errorf(lineNo, w, "invalid_address", "Нет хоста в адресе «%s»", addr)
		return
	}
	if msg := lintCheckHost(host, false); msg != "" {
		l.errorf(lineNo, w, "invalid_address", "%s: «%s»", msg, addr)
		return
	}
	if !hasPort {
		if !portOptional {
			l.errorf(lineNo, w, "invalid_port", "Нет порта в адресе «%s»", addr)
		} else {
			l.warnf(lineNo, w, "server_port_missing", "Нет порта в адресе «%s»: будет использован порт клиента", addr)
		}
		return
	}
	value := port
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		value = value[1:] // relative port mapping
		if n, err := strconv.Atoi(value); err != nil || n < 0 || n > 65535 {
			l.errorf(lineNo, w, "invalid_port", "Неверный порт «%s»", port)
		}
		return
	}
	if _, ok := lintParsePort(value); !ok {
		l.errorf(lineNo, w, "invalid_port", "Неверный порт «%s» (1–65535)", port)
	}
}

// bindAddresses validates a comma-separated bind address list and records
// TCP listener ports of frontend/listen sections.
func (l *linter) bindAddresses(lineNo int, w lintWord, record bool) {
	for _, addr := range strings.Split(w.text, ",") {
		if addr == "" {
			l.errorf(lineNo, w, "invalid_address", "Пустой адрес в bind")
			continue
		}
		if strings.Contains(addr, "$") {
			continue
		}
		family, rest := lintSplitFamily(addr)
		if family == "local" || strings.HasPrefix(rest, "/") {
			continue
		}
		host, port, hasPort, msg := lintSplitHostPort(rest)
		if msg != "" {
			l.errorf(lineNo, w, "invalid_address", "%s: «%s»", msg, addr)
			continue
		}
		if msg := lintCheckHost(host, true); msg != "" {
			l.errorf(lineNo, w, "invalid_address", "%s: «%s»", msg, addr)
			continue
		}
		if !hasPort || port == "" {
			l.errorf(lineNo, w, "invalid_port", "Нет порта в bind «%s»", addr)
			continue
		}
		low, high, ok := lintParsePortRange(port)
		if !ok {
			l.errorf(lineNo, w, "invalid_port", "Неверный порт «%s» (1–65535)", port)
			continue
		}
		if !record || family == "udp" {
			continue
		}
		for p := low; p <= high; p++ {
			if _, exists := l.ports[p]; exists {
				continue
			}
			if len(l.ports) >= maxFirewallPorts {
				if !l.portOverflow {
					l.errorf(lineNo, w, "too_many_ports", "Больше %d портов в bind: firewall не сможет их открыть", maxFirewallPorts)
				}
				l.portOverflow = true
				break
			}
			l.ports[p] = struct{}{}
		}
	}
}

func lintSplitFamily(addr string) (family, rest string) {
	if i := strings.Index(addr, "@"); i > 0 {
		if f, ok := lintAddrFamilies[strings.ToLower(addr[:i])]; ok {
			return f, addr[i+1:]
		}
	}
	return "", addr
}

func lintSplitHostPort(s string) (host, port string, hasPort bool, msg string) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", false, "Незакрытая «[»"
		}
		host, rest := s[1:end], s[end+1:]
		if net.ParseIP(host) == nil || !strings.Contains(host, ":") {
			return "", "", false, "Неверный IPv6-адрес"
		}
		if rest == "" {
			return host, "", false, ""
		}
		if rest[0] != ':' {
			return "", "", false, "Ожидалось «:порт» после «]»"
		}
		return host, rest[1:], true, ""
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, "", false, ""
	}
	return s[:i], s[i+1:], true, ""
}

func lintCheckHost(host string, bind bool) string {
	if host == "" || host == "*" {
		if bind {
			return ""
		}
		return "Нет хоста"
	}
	if strings.Contains(host, ":") {
		if net.ParseIP(host) == nil {
			return "Неверный IPv6-адрес (используйте [адрес]:порт)"
		}
		return ""
	}
	numeric := true
	for _, r := range host {
		if !(r >= '0' && r <= '9') && r != '.' {
			numeric = false
		}
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '-' && r != '_' {
			return "Недопустимые символы в хосте"
		}
	}
	if numeric && net.ParseIP(host) == nil {
		return "Неверный IPv4-адрес"
	}
	return ""
}

func lintParsePort(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

func lintParsePortRange(s string) (int, int, bool) {
	if a, b, ok := strings.Cut(s, "-"); ok {
		low, okA := lintParsePort(a)
		high, okB := lintParsePort(b)
		if !okA || !okB || high < low {
			return 0, 0, false
		}
		return low, high, true
	}
	p, ok := lintParsePort(s)
	return p, p, ok
}

// lintTokenize splits a line the way HAProxy does: whitespace-separated
// words, single/double quotes, backslash escapes and '#' comments. It returns
// the column of an unterminated quote, or 0.
func lintTokenize(line string) ([]lintWord, int) {
	runes := []rune(line)
	var words []lintWord
	space := func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' }
	i := 0
	for i < len(runes) {
		for i < len(runes) && space(runes[i]) {
			i++
		}
		if i >= len(runes) || runes[i] == '#' {
			break
		}
		start := i
		var b strings.Builder
		comment := false
		quoted := false
		for i < len(runes) && !space(runes[i]) {
			c := runes[i]
			if c == '#' {
				comment = true
				break
			}
			if c == '\\' {
				quoted = true
				if i+1 < len(runes) {
					b.WriteRune(runes[i+1])
					i += 2
				} else {
					i++
				}
				continue
			}
			if c == '"' || c == '\'' {
				quoted = true
				quoteAt := i
				i++
				closed := false
				for i < len(runes) {
					if c == '"' && runes[i] == '\\' && i+1 < len(runes) {
						b.WriteRune(runes[i+1])
						i += 2
						continue
					}
					if runes[i] == c {
						closed = true
						i++
						break
					}
					b.WriteRune(runes[i])
					i++
				}
				if !closed {
					words = append(words, lintWord{text: b.String(), col: start + 1, end: i + 1})
					return words, quoteAt + 1
				}
				continue
			}
			b.WriteRune(c)
			i++
		}
		if i > start {
			words = append(words, lintWord{text: b.String(), col: start + 1, end: i + 1, quoted: quoted})
		}
		if comment {
			break
		}
	}
	return words, 0
}
