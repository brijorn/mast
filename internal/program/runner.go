package program

import (
	"fmt"
	"strings"
	"unicode"
)

func splitRunnerCommand(command string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	inToken := false

	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			inToken = true
			escaped = false
			continue
		}

		if quote != '\'' && r == '\\' {
			escaped = true
			inToken = true
			continue
		}

		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			inToken = true
			continue
		}

		switch {
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case unicode.IsSpace(r):
			if inToken {
				parts = append(parts, current.String())
				current.Reset()
				inToken = false
			}
		default:
			current.WriteRune(r)
			inToken = true
		}
	}

	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %q quote", quote)
	}
	if inToken {
		parts = append(parts, current.String())
	}
	return parts, nil
}
