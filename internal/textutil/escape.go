// Package textutil protects human-readable CLI diagnostics. Structured JSON
// already escapes controls; the dashboard has its own streaming log allowlist.
package textutil

import (
	"strconv"
	"strings"
	"unicode"
)

// EscapeControls retains readable lines/tabs and normal Unicode, but renders
// terminal controls and bidi formatting literally instead of executing them.
func EscapeControls(text string) string {
	var out strings.Builder
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			quoted := strconv.QuoteRuneToASCII(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
