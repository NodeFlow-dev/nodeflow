package agent

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxValidationDetailBytes bounds the haproxy -c excerpt reported to the
// Panel so a pathological config cannot bloat apply reports.
const maxValidationDetailBytes = 1500

var (
	ansiEscapePattern     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	validateTempPathRegex = regexp.MustCompile(`(?:[^\s'"\[\]]*/)?\.nodeflow-validate-[A-Za-z0-9]+`)
	sensitiveLinePattern  = regexp.MustCompile(`(?i)password|secret`)
)

// ValidationDetail returns the operator-facing excerpt of a failed
// haproxy -c run: [ALERT]/[WARNING] lines (falling back to all non-empty
// lines), ANSI stripped, the temporary validation file renamed to
// haproxy.cfg and the result truncated to maxValidationDetailBytes.
func ValidationDetail(output []byte, tempPath string) string {
	text := ansiEscapePattern.ReplaceAllString(string(output), "")
	text = strings.ReplaceAll(text, "\r", "")
	if tempPath != "" {
		text = strings.ReplaceAll(text, tempPath, "haproxy.cfg")
		text = strings.ReplaceAll(text, filepath.Base(tempPath), "haproxy.cfg")
	}
	text = validateTempPathRegex.ReplaceAllString(text, "haproxy.cfg")
	var alerts, other []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || sensitiveLinePattern.MatchString(line) {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "[ALERT]") || strings.Contains(upper, "[WARNING]") {
			alerts = append(alerts, line)
		} else {
			other = append(other, line)
		}
	}
	lines := alerts
	if len(lines) == 0 {
		lines = other
	}
	return truncateUTF8(strings.Join(lines, "\n"), maxValidationDetailBytes)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	const marker = "\n…"
	cut := limit - len(marker)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimRight(value[:cut], "\n") + marker
}

// validationError keeps the sanitized haproxy -c excerpt next to the raw
// failure so ApplyRevision can report it without re-parsing.
type validationError struct {
	detail string
	output string
}

func (e *validationError) Error() string { return "haproxy validation failed: " + e.output }
