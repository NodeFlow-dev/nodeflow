package panel

import (
	"regexp"
	"strconv"
	"strings"
)

// Keyword-level checks shared in spirit with the browser linter
// (frontend/src/features/haproxy-editor/haproxyModel.ts). Both read the same
// haproxyKeywords.json and must report the same codes.

var lintDurationValue = regexp.MustCompile(`^[0-9]+(us|ms|s|m|h|d)?$`)

// Server options whose value is a delay / a positive count.
var lintServerDurationKeywords = lintSet("inter", "fastinter", "downinter", "slowstart", "agent-inter", "pool-purge-delay")

// Directives whose trailing words are free text, expressions or lists where a
// final ',' or ';' may be legitimate.
var lintFreeTextKeywords = lintSet(
	"acl", "bind", "capture", "description", "email-alert", "error-log-format", "errorfile", "errorfiles",
	"errorloc", "errorloc302", "errorloc303", "filter", "http-after-response", "http-check", "http-error",
	"http-request", "http-response", "log-format", "log-format-sd", "log-tag", "monitor-uri", "node",
	"presetenv", "redirect", "set-var", "set-var-fmt", "setenv", "stats", "tcp-check", "tcp-request",
	"tcp-response", "unique-id-format", "unique-id-header", "use_backend", "default_backend", "use-server",
	"stick", "balance", "command", "user", "group", "lua-load", "lua-load-per-thread", "lua-prepend-path",
	"hash-type", "server", "server-template", "default-server", "option", "timeout", "mode", "log",
)

func lintNegatable(section, kw string) bool {
	if section == "global" {
		return lintKW.globalNegatable[kw]
	}
	return kw == "option" || kw == "log"
}

func (l *linter) unknownDirective(lineNo int, section string, kw lintWord) {
	if base, n := lintTrailingPunct(kw.text); n > 0 && lintKnownKeyword(section, base) {
		l.errorf(lineNo, lintPunctWord(kw, n), "stray_punctuation", "Лишний символ «%s» после «%s»", kw.text[len(base):], base)
		return
	}
	if section == "global" || lintIsProxySection(section) {
		l.errorf(lineNo, kw, "unknown_directive", "Неизвестная директива «%s» в секции %s", kw.text, section)
		return
	}
	l.warnf(lineNo, kw, "unknown_directive", "Неизвестная директива «%s» в секции %s", kw.text, section)
}

// proxyKeywordAllowed applies the HAProxy 4.1 keyword matrix. It reports an
// error and returns false when the keyword (or its sub-keyword) is not
// accepted in this section type.
func (l *linter) proxyKeywordAllowed(lineNo int, section string, words []lintWord) bool {
	kw := words[0]
	if !l.sectionAllows(lineNo, section, kw, kw.text, lintKW.proxy[kw.text]) {
		return false
	}
	if kw.text == "timeout" || kw.text == "option" || len(words) < 2 {
		return true
	}
	sub := words[1]
	if flags, ok := lintKW.proxySub[kw.text+" "+sub.text]; ok {
		return l.sectionAllows(lineNo, section, sub, kw.text+" "+sub.text, flags)
	}
	if !lintKW.proxyStrictSub[kw.text] {
		return true
	}
	if base, n := lintTrailingPunct(sub.text); n > 0 {
		if _, ok := lintKW.proxySub[kw.text+" "+base]; ok {
			l.errorf(lineNo, lintPunctWord(sub, n), "stray_punctuation", "Лишний символ «%s» после «%s»", sub.text[len(base):], base)
			return false
		}
	}
	l.errorf(lineNo, sub, "unknown_keyword", "Неизвестный параметр «%s» у %s", sub.text, kw.text)
	return false
}

func (l *linter) sectionAllows(lineNo int, section string, w lintWord, name, flags string) bool {
	allowed, anonymous := lintSectionAllowed(flags, section, l.namedDefaults)
	if allowed {
		return true
	}
	if anonymous {
		l.errorf(lineNo, w, "keyword_not_allowed", "«%s» недопустим в безымянной секции defaults (задайте ей имя)", name)
	} else {
		l.errorf(lineNo, w, "keyword_not_allowed", "«%s» недопустим в секции %s", name, section)
	}
	return false
}

func (l *linter) option(lineNo int, section string, words []lintWord, modifier string) {
	if len(words) < 2 {
		l.errorf(lineNo, words[0], "option_missing_name", "У option не указано имя")
		return
	}
	name := words[1]
	flags, ok := lintKW.options[name.text]
	if !ok {
		if base, n := lintTrailingPunct(name.text); n > 0 && lintKW.options[base] != "" {
			l.errorf(lineNo, lintPunctWord(name, n), "unknown_option", "Неизвестная опция «%s» — лишний символ «%s»", name.text, name.text[len(base):])
		} else {
			l.errorf(lineNo, name, "unknown_option", "Неизвестная опция «%s»", name.text)
		}
		return
	}
	if modifier != "" && lintKW.optionsNoNeg[name.text] {
		l.errorf(lineNo, name, "negation_not_supported", "Префикс «%s» не поддерживается для option %s", modifier, name.text)
		return
	}
	if allowed, _ := lintSectionAllowed(flags, section, true); !allowed {
		l.warnf(lineNo, name, "option_ignored", "option %s игнорируется в секции %s", name.text, section)
	}
	switch lintKW.optionArgs[name.text] {
	case "none":
		if len(words) > 2 {
			l.errorf(lineNo, words[2], "too_many_args", "option %s не принимает аргументов", name.text)
		}
	case "clf":
		if len(words) > 2 && words[2].text != "clf" {
			l.errorf(lineNo, words[2], "invalid_option_arg", "option %s принимает только «clf»", name.text)
		} else if len(words) > 3 {
			l.errorf(lineNo, words[3], "too_many_args", "Лишний аргумент option %s", name.text)
		}
	}
}

func (l *linter) mode(lineNo int, section string, words []lintWord) {
	if len(words) < 2 {
		l.errorf(lineNo, words[0], "invalid_mode", "У mode не указан режим (tcp или http)")
		return
	}
	value := words[1]
	flags, ok := lintKW.modes[value.text]
	switch {
	case value.text == "health":
		l.errorf(lineNo, value, "invalid_mode", "mode health больше не поддерживается")
	case !ok:
		if base, n := lintTrailingPunct(value.text); n > 0 && lintKW.modes[base] != "" {
			l.errorf(lineNo, lintPunctWord(value, n), "stray_punctuation", "Лишний символ «%s» после «%s»", value.text[len(base):], base)
		} else {
			l.errorf(lineNo, value, "invalid_mode", "Неизвестный режим «%s» (tcp, http, log, spop)", value.text)
		}
	default:
		if allowed, _ := lintSectionAllowed(flags, section, true); !allowed {
			l.errorf(lineNo, value, "invalid_mode", "mode %s недопустим в секции %s", value.text, section)
		} else if len(words) > 2 {
			l.errorf(lineNo, words[2], "too_many_args", "Лишний аргумент mode")
		}
	}
}

func (l *linter) balance(lineNo int, words []lintWord) {
	if len(words) < 2 {
		l.errorf(lineNo, words[0], "invalid_balance", "У balance не указан алгоритм")
		return
	}
	algo := words[1]
	name, _, _ := strings.Cut(algo.text, "(")
	if lintKW.balance[name] {
		return
	}
	if base, n := lintTrailingPunct(algo.text); n > 0 && lintKW.balance[base] {
		l.errorf(lineNo, lintPunctWord(algo, n), "stray_punctuation", "Лишний символ «%s» после «%s»", algo.text[len(base):], base)
		return
	}
	l.errorf(lineNo, algo, "invalid_balance", "Неизвестный алгоритм balance «%s»", algo.text)
}

func (l *linter) hashType(lineNo int, words []lintWord) {
	sets := []map[string]bool{lintKW.hashMethods, lintKW.hashFunctions, lintKW.hashModifiers}
	if len(words) < 2 {
		l.errorf(lineNo, words[0], "invalid_hash_type", "hash-type: укажите consistent или map-based")
		return
	}
	for i, w := range words[1:] {
		if i >= len(sets) {
			l.errorf(lineNo, w, "too_many_args", "Лишний аргумент hash-type")
			return
		}
		if sets[i][w.text] {
			continue
		}
		if base, n := lintTrailingPunct(w.text); n > 0 && sets[i][base] {
			l.errorf(lineNo, lintPunctWord(w, n), "stray_punctuation", "Лишний символ «%s» после «%s»", w.text[len(base):], base)
		} else {
			l.errorf(lineNo, w, "invalid_hash_type", "Неверный аргумент hash-type «%s»", w.text)
		}
		return
	}
}

// logTarget validates "log <target> [len n] [format f] [sample r:s]
// [profile p] <facility> [<level> [<minlevel>]]".
func (l *linter) logTarget(lineNo int, section string, words []lintWord) {
	if len(words) < 2 {
		l.errorf(lineNo, words[0], "log_missing_target", "У log не указан адрес")
		return
	}
	if words[1].text == "global" && section != "global" {
		if len(words) > 2 {
			l.errorf(lineNo, words[2], "too_many_args", "Лишний аргумент log global")
		}
		return
	}
	i := 2
	for i < len(words) && lintKW.logOptions[words[i].text] {
		i += 2
	}
	if i >= len(words) {
		l.errorf(lineNo, words[len(words)-1], "log_missing_facility", "У log не указан facility (например local0)")
		return
	}
	for n, w := range words[i:] {
		set, what := lintKW.logLevels, "уровень"
		if n == 0 {
			set, what = lintKW.logFacilities, "facility"
		}
		if n > 2 {
			l.errorf(lineNo, w, "too_many_args", "Лишний аргумент log «%s»", w.text)
			return
		}
		if strings.Contains(w.text, "$") || set[w.text] {
			continue
		}
		if base, cut := lintTrailingPunct(w.text); cut > 0 && set[base] {
			l.errorf(lineNo, lintPunctWord(w, cut), "stray_punctuation", "Лишний символ «%s» после «%s»", w.text[len(base):], base)
		} else {
			l.errorf(lineNo, w, "invalid_log", "Неизвестный %s log «%s»", what, w.text)
		}
		return
	}
}

// lintKeywordArgs walks "<kw> [value…]" option lists of server/bind lines.
func (l *linter) keywordArgs(lineNo int, words []lintWord, start int, table map[string]int, code, what string, check func(kw string, value lintWord)) {
	for i := start; i < len(words); {
		w := words[i]
		name, _, _ := strings.Cut(w.text, "(")
		n, ok := table[name]
		if !ok {
			if base, cut := lintTrailingPunct(w.text); cut > 0 {
				if _, known := table[base]; known {
					l.errorf(lineNo, lintPunctWord(w, cut), "stray_punctuation", "Лишний символ «%s» после «%s»", w.text[len(base):], base)
					i++
					continue
				}
			}
			l.errorf(lineNo, w, code, "Неизвестный параметр %s «%s»", what, w.text)
			i++
			continue
		}
		if n < 0 { // server "source <addr> [usesrc <addr>] [interface <name>]"
			i += 2
			for i+1 < len(words) && (words[i].text == "usesrc" || words[i].text == "interface") {
				i += 2
			}
			continue
		}
		if i+n >= len(words) && n > 0 {
			l.errorf(lineNo, w, "missing_value", "У параметра «%s» нет значения", w.text)
			return
		}
		if n > 0 && check != nil {
			check(name, words[i+1])
		}
		i += 1 + n
	}
}

func (l *linter) serverKeywords(lineNo int, words []lintWord, start int) {
	l.keywordArgs(lineNo, words, start, lintKW.server, "unknown_server_keyword", "server", func(kw string, value lintWord) {
		if strings.Contains(value.text, "$") {
			return
		}
		switch {
		case lintServerDurationKeywords[kw]:
			if !lintDurationValue.MatchString(value.text) {
				l.errorf(lineNo, value, "invalid_duration", "«%s» — неверное время для %s (пример: 5s, 500ms)", value.text, kw)
			}
		case kw == "fall" || kw == "rise":
			if n, err := strconv.Atoi(value.text); err != nil || n < 1 {
				l.errorf(lineNo, value, "invalid_number", "%s ожидает целое число ≥ 1", kw)
			}
		case kw == "weight":
			if n, err := strconv.Atoi(value.text); err != nil || n < 0 || n > 256 {
				l.errorf(lineNo, value, "invalid_weight", "weight — целое число 0–256")
			}
		}
	})
}

func (l *linter) bindKeywords(lineNo int, words []lintWord, start int) {
	l.keywordArgs(lineNo, words, start, lintKW.bind, "unknown_bind_keyword", "bind", nil)
}

// strayTail reports a bare trailing ',' or ';' on the last word of a
// directive whose arguments are plain values (maxconn 100; retries 3,).
func (l *linter) strayTail(lineNo int, words []lintWord, before int) {
	if lintFreeTextKeywords[words[0].text] || len(words) < 2 {
		return
	}
	last := words[len(words)-1]
	if last.quoted || strings.Contains(last.text, "%[") || strings.ContainsAny(last.text, "{}") {
		return
	}
	base := strings.TrimRight(last.text, ",;")
	if base == last.text {
		return
	}
	punct := lintPunctWord(last, len(last.text)-len(base))
	for _, issue := range l.issues[before:] {
		if issue.Line == lineNo && issue.Column <= punct.col && punct.col < issue.EndColumn {
			return
		}
	}
	l.errorf(lineNo, punct, "stray_punctuation", "Лишний символ «%s» в конце строки", last.text[len(base):])
}
