package program

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// buildTimeout bounds a native build. gocv links OpenCV and can take minutes;
// this is a backstop against a build that hangs rather than a target time.
const buildTimeout = 20 * time.Minute

func (s *Store) buildsDir() string     { return filepath.Join(s.root, "builds") }
func (s *Store) buildCacheDir() string { return filepath.Join(s.buildsDir(), "cache") }

// BuildFromSource builds a program's native bundle from shipped source and
// registers it, reusing a previous build when the source, command, and target
// platform are unchanged (build-if-stale). sources are files laid out as
// {repo}/{relpath}; recipe.Workdir is relative to that root; recipe.Artifacts
// are globs relative to Workdir that become the registered bundle.
func (s *Store) BuildFromSource(recipe BuildRecipe, sources []UploadFile) (*Program, error) {
	if strings.TrimSpace(recipe.Command) == "" {
		return nil, errors.New("build recipe command required")
	}
	if strings.TrimSpace(recipe.Entry.Command) == "" {
		return nil, errors.New("build recipe entry command required")
	}
	if len(sources) == 0 {
		return nil, errors.New("build recipe requires source files")
	}

	// RegisterUpload writes into bundles/; the store creates it at init, but a
	// build may be the first write, so ensure it exists.
	if err := os.MkdirAll(s.bundleDir(), 0o755); err != nil {
		return nil, err
	}

	srcRoot, cleanup, err := s.writeSource(sources)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// The cache key binds the exact source bytes to the command and the target
	// platform, so a darwin build and a linux build of the same source never
	// collide and a source or recipe change misses the cache.
	srcHash, err := hashDir(srcRoot)
	if err != nil {
		return nil, err
	}
	key := buildCacheKey(srcHash, recipe.Command, runtime.GOOS, runtime.GOARCH)

	if prog, ok := s.cachedBuild(key); ok {
		return prog, nil
	}

	workdir := filepath.Join(srcRoot, filepath.FromSlash(recipe.Workdir))
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("build workdir %q not found in shipped source", recipe.Workdir)
	}

	if out, err := runBuildCommand(recipe.Command, workdir); err != nil {
		return nil, fmt.Errorf("build failed: %w\n%s", err, out)
	}

	files, err := collectArtifacts(workdir, recipe.Artifacts)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("build produced no artifacts matching %v", recipe.Artifacts)
	}

	prog, err := s.RegisterUpload(RegisterUploadOptions{
		Name:                recipe.Name,
		Slug:                recipe.Slug,
		ConfigFile:          recipe.ConfigFile,
		ConfigMappings:      recipe.ConfigMappings,
		Entry:               recipe.Entry,
		FinishesOnCleanExit: recipe.FinishesOnCleanExit,
		Files:               files,
	})
	if err != nil {
		return nil, err
	}

	if err := s.recordBuild(key, prog.ID); err != nil {
		return nil, err
	}
	return prog, nil
}

// writeSource materializes shipped source files into a fresh build workspace,
// guarding against path traversal, and returns the source root plus a cleanup.
func (s *Store) writeSource(sources []UploadFile) (root string, cleanup func(), err error) {
	if err := os.MkdirAll(s.buildsDir(), 0o755); err != nil {
		return "", nil, err
	}
	work, err := os.MkdirTemp(s.buildsDir(), "src-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(work) }
	for _, f := range sources {
		rel := filepath.FromSlash(f.Path)
		if strings.Contains(rel, "..") {
			cleanup()
			return "", nil, fmt.Errorf("invalid source path: %q", f.Path)
		}
		target := filepath.Join(work, rel)
		if !strings.HasPrefix(
			filepath.Clean(target)+string(os.PathSeparator),
			filepath.Clean(work)+string(os.PathSeparator),
		) {
			cleanup()
			return "", nil, fmt.Errorf("invalid source path: %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		in, err := f.open()
		if err != nil {
			_ = out.Close()
			cleanup()
			return "", nil, err
		}
		_, copyErr := io.Copy(out, in)
		_ = in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			cleanup()
			return "", nil, copyErr
		}
		if closeErr != nil {
			cleanup()
			return "", nil, closeErr
		}
	}
	return work, cleanup, nil
}

// runBuildCommand runs a recipe's build command in workdir through a login shell
// so the host's toolchain (Go, OpenCV via pkg-config) is on PATH the way an
// interactive build sees it. It returns combined output for diagnosis.
func runBuildCommand(command, workdir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		// A login shell loads the profile that puts go and pkg-config on PATH;
		// a service's own environment often does not.
		cmd = exec.CommandContext(ctx, "bash", "-lc", command)
	}
	cmd.Dir = workdir
	return cmd.CombinedOutput()
}

// collectArtifacts resolves the recipe's artifact globs against workdir and
// returns them as upload files at their workdir-relative paths.
func collectArtifacts(workdir string, globs []string) ([]UploadFile, error) {
	seen := map[string]bool{}
	var files []UploadFile
	for _, glob := range globs {
		matches, err := filepath.Glob(filepath.Join(workdir, filepath.FromSlash(glob)))
		if err != nil {
			return nil, fmt.Errorf("bad artifact glob %q: %w", glob, err)
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			rel, err := filepath.Rel(workdir, match)
			if err != nil {
				return nil, err
			}
			slashRel := filepath.ToSlash(rel)
			if seen[slashRel] {
				continue
			}
			seen[slashRel] = true
			path := match
			files = append(files, UploadFile{
				Path: slashRel,
				Open: func() (io.ReadCloser, error) { return os.Open(path) },
			})
		}
	}
	return files, nil
}

func buildCacheKey(srcHash, command, goos, goarch string) string {
	sum := sha256.Sum256([]byte(srcHash + "\x00" + command + "\x00" + goos + "/" + goarch))
	return hex.EncodeToString(sum[:])
}

// cachedBuild returns the program a prior build produced for this key, but only
// if that program is still registered — a bundle deleted by a slug replacement
// invalidates the cache entry.
func (s *Store) cachedBuild(key string) (*Program, bool) {
	data, err := os.ReadFile(filepath.Join(s.buildCacheDir(), key))
	if err != nil {
		return nil, false
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	prog, ok := s.programs[id]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	return &prog, true
}

func (s *Store) recordBuild(key, programID string) error {
	if err := os.MkdirAll(s.buildCacheDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.buildCacheDir(), key), []byte(programID), 0o644)
}
