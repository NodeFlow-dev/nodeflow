package panel

import (
	"strings"
	"unicode/utf8"
)

// maxConfigErrorDetailBytes bounds the stored apply failure excerpt.
const maxConfigErrorDetailBytes = 2048

// applyReportErrorDetail extracts the optional operator-facing failure
// excerpt newer Agents send as details.error_detail. Older Agents omit it.
func applyReportErrorDetail(details map[string]any) string {
	raw, _ := details["error_detail"].(string)
	raw = strings.ToValidUTF8(strings.TrimSpace(raw), "")
	if len(raw) > maxConfigErrorDetailBytes {
		cut := maxConfigErrorDetailBytes
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}
		raw = raw[:cut]
	}
	return raw
}
