//go:build !windows

package cli

// serviceFileBytes writes the service definition verbatim. Only Windows needs
// a particular encoding.
func serviceFileBytes(content string) []byte {
	return []byte(content)
}
