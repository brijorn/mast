package program

import (
	"strings"
	"unicode"
)

// toSlug converts a program name to a URL-safe slug.
//
//	"My App 2.0" → "my-app-2-0"
func toSlug(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		} else if out.Len() > 0 {
			// Collapse consecutive non-alphanumeric runs to a single dash.
			s := out.String()
			if s[len(s)-1] != '-' {
				out.WriteRune('-')
			}
		}
	}
	return strings.TrimRight(out.String(), "-")
}
