package program

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// artifactMaxBytes bounds one served file. Evidence is screenshots and small
// JSON reports; anything larger is a program writing something this endpoint
// was not meant to hand out, and streaming it would let a log row pull a video
// through the API.
const artifactMaxBytes = 32 << 20

// ErrArtifactNotFound reports a path that does not name a readable file inside
// the run's workspace.
var ErrArtifactNotFound = errors.New("artifact not found")

// OpenArtifact opens a file belonging to one run's workspace.
//
// Runs report evidence by absolute path because a human reading a log line
// needs to open it, but that path is only meaningful on the node holding the
// workspace — so the file has to be served from here rather than read by
// whoever is displaying the log.
//
// The path is accepted either absolute or workspace-relative and is resolved
// against the workspace before anything opens it. What comes back must still
// be inside that directory: a run is not a file server for the node, and
// "../../.." in a program's own event payload must not read the host's
// filesystem. Symlinks are resolved before the check for the same reason.
func (s *Store) OpenArtifact(id, name string) (io.ReadCloser, os.FileInfo, error) {
	s.mu.Lock()
	state := s.runs[id]
	s.mu.Unlock()
	if state == nil {
		return nil, nil, errors.New("run not found")
	}

	workspace, err := filepath.EvalSymlinks(state.run.Workspace)
	if err != nil {
		return nil, nil, ErrArtifactNotFound
	}

	requested := strings.TrimSpace(name)
	if requested == "" {
		return nil, nil, ErrArtifactNotFound
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(workspace, requested)
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return nil, nil, ErrArtifactNotFound
	}
	// Compare on the cleaned, symlink-resolved paths, with the separator so a
	// sibling directory sharing the workspace's name prefix cannot pass.
	if resolved != workspace && !strings.HasPrefix(resolved, workspace+string(filepath.Separator)) {
		return nil, nil, ErrArtifactNotFound
	}

	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || info.Size() > artifactMaxBytes {
		return nil, nil, ErrArtifactNotFound
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, nil, ErrArtifactNotFound
	}
	return file, info, nil
}
