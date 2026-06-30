package program

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RegisterUpload registers a program from a set of uploaded files. Files are
// written directly into a temporary directory inside the bundle store, then
// atomically moved to the final content-addressed path.
//
// Re-uploading a program with the same slug replaces the current bundle.
// Running instances are not affected because they execute from a copied workspace.
func (s *Store) RegisterUpload(opts RegisterUploadOptions) (*Program, error) {
	if opts.Entry.Command == "" {
		return nil, errors.New("entry command required")
	}
	if len(opts.Files) == 0 {
		return nil, errors.New("at least one file required")
	}

	// Create a temporary directory inside bundleDir so that os.Rename later
	// stays on the same filesystem (avoiding a cross-device link error).
	tmp, err := os.MkdirTemp(s.bundleDir(), "upload-*")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmp)
		}
	}()

	for _, f := range opts.Files {
		rel := filepath.FromSlash(f.Path)
		if strings.Contains(rel, "..") {
			return nil, fmt.Errorf("invalid file path: %q", f.Path)
		}
		target := filepath.Join(tmp, rel)
		// Guard against path traversal after joining.
		if !strings.HasPrefix(
			filepath.Clean(target)+string(os.PathSeparator),
			filepath.Clean(tmp)+string(os.PathSeparator),
		) {
			return nil, fmt.Errorf("invalid file path: %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, err
		}
		in, err := f.open()
		if err != nil {
			_ = out.Close()
			return nil, err
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		outErr := out.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if inErr != nil {
			return nil, inErr
		}
		if outErr != nil {
			return nil, outErr
		}
	}

	id, err := hashDir(tmp)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "unnamed"
	}
	slug := strings.TrimSpace(opts.Slug)
	if slug == "" {
		slug = toSlug(name)
	}
	s.mu.Lock()
	previous, hasPrevious := s.programBySlugLocked(slug)
	s.mu.Unlock()
	program := Program{
		ID:             id,
		Slug:           slug,
		Name:           name,
		ConfigFile:     opts.ConfigFile,
		ConfigMappings: opts.ConfigMappings,
		Entry:          opts.Entry,
		CreatedAt:      time.Now().UTC(),
	}

	bundlePath := s.bundlePath(id)
	// Remove the target path if it already exists (same content = idempotent).
	if err := os.RemoveAll(bundlePath); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, bundlePath); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(bundlePath, "mast-program.json"), program); err != nil {
		return nil, err
	}
	success = true

	var previousID string
	s.mu.Lock()
	if hasPrevious {
		previousID = previous.ID
		delete(s.programs, previousID)
	}
	s.programs[id] = program
	err = s.saveRegistryLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if previousID != "" && previousID != id {
		_ = os.RemoveAll(s.bundlePath(previousID))
	}

	return &program, nil
}

func (f UploadFile) open() (io.ReadCloser, error) {
	if f.Open != nil {
		return f.Open()
	}
	if f.Content == nil {
		return nil, fmt.Errorf("file content required: %s", f.Path)
	}
	return io.NopCloser(f.Content), nil
}

func (s *Store) UpdateProgram(id string, name string, slug string, mappings []ConfigMapping) (*Program, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.programs[id]
	if !ok {
		return nil, errors.New("program not found")
	}

	name = strings.TrimSpace(name)
	if name != "" {
		p.Name = name
	}
	slug = strings.TrimSpace(slug)
	if slug != "" {
		p.Slug = slug
	}
	p.ConfigMappings = mappings
	s.programs[id] = p

	if err := s.saveRegistryLocked(); err != nil {
		return nil, err
	}

	return &p, nil
}

func (s *Store) DeleteProgram(id string) error {
	s.mu.Lock()
	p, ok := s.programs[id]
	if !ok {
		p, ok = s.programBySlugLocked(id)
	}
	if !ok {
		s.mu.Unlock()
		return errors.New("program not found")
	}

	delete(s.programs, p.ID)
	if err := s.saveRegistryLocked(); err != nil {
		s.programs[p.ID] = p
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	return os.RemoveAll(s.bundlePath(p.ID))
}

func (s *Store) ListPrograms() []Program {
	s.mu.Lock()
	defer s.mu.Unlock()

	programs := make([]Program, 0, len(s.programs))
	for _, p := range s.programs {
		programs = append(programs, p)
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].CreatedAt.Before(programs[j].CreatedAt)
	})
	return programs
}

func (s *Store) programBySlugLocked(slug string) (Program, bool) {
	for _, p := range s.programs {
		if p.Slug == slug {
			return p, true
		}
	}
	return Program{}, false
}
