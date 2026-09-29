package panel

import (
	_ "embed"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// haproxy_keywords.json is a byte-for-byte copy of
// frontend/src/features/haproxy-editor/haproxyKeywords.json, which the
// browser linter imports; TestLintKeywordsJSONInSync keeps them identical.
//
//go:embed haproxy_keywords.json
var haproxyKeywordsJSON []byte

type lintKeywordData struct {
	Global struct {
		Keywords  []string `json:"keywords"`
		Prefixes  []string `json:"prefixes"`
		Negatable []string `json:"negatable"`
	} `json:"global"`
	// Section letters: d=defaults, n=named defaults only, f=frontend,
	// l=listen, b=backend.
	Proxy             map[string]string   `json:"proxy"`
	ProxySub          map[string]string   `json:"proxy_sub"`
	ProxyStrictSub    []string            `json:"proxy_strict_sub"`
	ProxyNegatable    []string            `json:"proxy_negatable"`
	Options           map[string]string   `json:"options"`
	OptionsNoNegation []string            `json:"options_no_negation"`
	OptionArgs        map[string]string   `json:"option_args"`
	Server            map[string]int      `json:"server"`
	Bind              map[string]int      `json:"bind"`
	Timeouts          map[string][]string `json:"timeouts"`
	Modes             map[string]string   `json:"modes"`
	Balance           []string            `json:"balance"`
	HashType          struct {
		Methods   []string `json:"methods"`
		Functions []string `json:"functions"`
		Modifiers []string `json:"modifiers"`
	} `json:"hash_type"`
	Log struct {
		Facilities []string `json:"facilities"`
		Levels     []string `json:"levels"`
		Options    []string `json:"options"`
	} `json:"log"`
	OtherSections map[string][]string `json:"other_sections"`
}

type lintKeywordSets struct {
	global          map[string]bool
	globalPrefixes  []string
	globalNegatable map[string]bool
	proxy           map[string]string
	proxySub        map[string]string
	proxyStrictSub  map[string]bool
	proxyNegatable  map[string]bool
	options         map[string]string
	optionsNoNeg    map[string]bool
	optionArgs      map[string]string
	server          map[string]int
	bind            map[string]int
	timeouts        map[string]map[string]bool
	modes           map[string]string
	balance         map[string]bool
	hashMethods     map[string]bool
	hashFunctions   map[string]bool
	hashModifiers   map[string]bool
	logFacilities   map[string]bool
	logLevels       map[string]bool
	logOptions      map[string]bool
	other           map[string]map[string]bool
}

var lintKW = mustLoadLintKeywords(haproxyKeywordsJSON)

func mustLoadLintKeywords(raw []byte) *lintKeywordSets {
	var d lintKeywordData
	if err := json.Unmarshal(raw, &d); err != nil {
		panic("haproxy_keywords.json: " + err.Error())
	}
	k := &lintKeywordSets{
		global:          lintSet(d.Global.Keywords...),
		globalPrefixes:  d.Global.Prefixes,
		globalNegatable: lintSet(d.Global.Negatable...),
		proxy:           d.Proxy,
		proxySub:        d.ProxySub,
		proxyStrictSub:  lintSet(d.ProxyStrictSub...),
		proxyNegatable:  lintSet(d.ProxyNegatable...),
		options:         d.Options,
		optionsNoNeg:    lintSet(d.OptionsNoNegation...),
		optionArgs:      d.OptionArgs,
		server:          d.Server,
		bind:            d.Bind,
		timeouts:        map[string]map[string]bool{},
		modes:           d.Modes,
		balance:         lintSet(d.Balance...),
		hashMethods:     lintSet(d.HashType.Methods...),
		hashFunctions:   lintSet(d.HashType.Functions...),
		hashModifiers:   lintSet(d.HashType.Modifiers...),
		logFacilities:   lintSet(d.Log.Facilities...),
		logLevels:       lintSet(d.Log.Levels...),
		logOptions:      lintSet(d.Log.Options...),
		other:           map[string]map[string]bool{},
	}
	for class, kinds := range d.Timeouts {
		k.timeouts[class] = lintSet(kinds...)
	}
	for section, words := range d.OtherSections {
		k.other[section] = lintSet(words...)
	}
	return k
}

func lintSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func lintIsProxySection(section string) bool {
	switch section {
	case "defaults", "frontend", "backend", "listen":
		return true
	}
	return false
}

// lintSectionAllowed reports whether a section letter set ("dflb", "nfl"…)
// admits the section. namedDefaults tells whether the current defaults
// section has a name (some rules are refused in anonymous defaults).
func lintSectionAllowed(flags, section string, namedDefaults bool) (allowed, anonymousDefaults bool) {
	switch section {
	case "defaults":
		if strings.Contains(flags, "d") {
			return true, false
		}
		if strings.Contains(flags, "n") {
			return namedDefaults, !namedDefaults
		}
		return false, false
	case "frontend":
		return strings.Contains(flags, "f"), false
	case "listen":
		return strings.Contains(flags, "l"), false
	case "backend":
		return strings.Contains(flags, "b"), false
	}
	return true, false
}

func lintKnownKeyword(section, kw string) bool {
	switch section {
	case "global":
		if lintKW.global[kw] {
			return true
		}
		for _, prefix := range lintKW.globalPrefixes {
			if strings.HasPrefix(kw, prefix) {
				return true
			}
		}
		return false
	case "defaults", "frontend", "backend", "listen":
		_, ok := lintKW.proxy[kw]
		return ok
	}
	return lintKW.other[section][kw]
}

// lintTrailingPunct returns the name without trailing ",;:." and the number
// of stripped runes.
func lintTrailingPunct(s string) (string, int) {
	trimmed := strings.TrimRight(s, ",;:.")
	return trimmed, utf8.RuneCountInString(s) - utf8.RuneCountInString(trimmed)
}

// lintPunctWord narrows w to its trailing punctuation (for markers).
func lintPunctWord(w lintWord, n int) lintWord {
	if w.quoted || n == 0 {
		return w
	}
	return lintWord{text: w.text, col: w.end - n, end: w.end}
}
