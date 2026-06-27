package program

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const VersionsFileName = "versions.json"

// versionsFile is the on-disk format for versions.json.
// It maps each program slug to the content-hash ID of its current bundle.
type versionsFile struct {
	Versions map[string]string `json:"versions"`
}

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

func (s *Store) versionsPath() string {
	return filepath.Join(s.root, VersionsFileName)
}

func (s *Store) loadVersions() error {
	f, err := os.Open(s.versionsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	var vf versionsFile
	if err := json.NewDecoder(f).Decode(&vf); err != nil {
		return err
	}
	if vf.Versions != nil {
		s.versions = vf.Versions
	}
	return nil
}

// saveVersionsLocked writes versions.json. Must be called with s.mu held.
func (s *Store) saveVersionsLocked() error {
	return writeJSON(s.versionsPath(), versionsFile{Versions: s.versions})
}
