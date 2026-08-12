package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/program"
)

// buildClient forwards a build request to a peer. A native gocv build can take
// minutes, so this timeout is well above a normal API call.
var buildClient = &http.Client{Timeout: 30 * time.Minute}

// sourceMaxMemory is how much of a build's multipart body is buffered in memory
// before overflow spills to temp files; source trees are tens of MB.
const sourceMaxMemory = 64 << 20

// BuildProgram builds a program's native bundle from source shipped in the
// request and registers it, returning the resulting program. It runs on the
// node that will execute the program (the phone's owner), because gocv cannot
// be cross-compiled. Only a connected peer or the local host may call it — it
// executes the recipe's build command.
func (s *Server) BuildProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.isTrustedBuildCaller(r) {
		http.Error(w, "build is restricted to connected peers", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(sourceMaxMemory); err != nil {
		http.Error(w, "parse build request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var recipe program.BuildRecipe
	if err := json.Unmarshal([]byte(r.FormValue("recipe")), &recipe); err != nil {
		http.Error(w, "invalid build recipe: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Each source file's relative path travels as its multipart field NAME, not
	// its filename: Go runs filepath.Base on a part's filename, which would flatten
	// the whole tree and lose the workdir. Field names are preserved verbatim.
	var sources []program.UploadFile
	if r.MultipartForm != nil {
		for name, headers := range r.MultipartForm.File {
			for _, header := range headers {
				header := header
				sources = append(sources, program.UploadFile{
					Path: name,
					Open: func() (io.ReadCloser, error) { return header.Open() },
				})
			}
		}
	}

	prog, err := s.programs.BuildFromSource(recipe, sources)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(prog); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// isTrustedBuildCaller allows the local host and any known peer node to request
// a build. The build command comes from the program's in-repo recipe, but the
// endpoint still executes it, so an unknown caller must not reach it.
func (s *Server) isTrustedBuildCaller(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	if s.node == nil {
		return false
	}
	for _, n := range s.node.ListNodes() {
		if n.Local {
			continue
		}
		if strings.TrimSpace(n.Addr) == host {
			return true
		}
	}
	return false
}

// buildOnPeer packs the recipe's source repos and asks the peer to build them,
// returning the peer's content-addressed program id for the freshly built
// bundle (or a cached one, when the peer's build cache still holds it).
func (s *Server) buildOnPeer(ctx context.Context, base string, recipe program.BuildRecipe) (string, error) {
	sources, err := packSource(recipe.Sources, recipe.Workdir)
	if err != nil {
		return "", err
	}
	if len(sources) == 0 {
		return "", fmt.Errorf("no source found for %v under %s", recipe.Sources, sourceRoot())
	}

	buildCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var writeErr error
		defer func() { _ = pw.CloseWithError(writeErr) }()
		recipeJSON, err := json.Marshal(recipe)
		if err != nil {
			writeErr = err
			return
		}
		if err := mw.WriteField("recipe", string(recipeJSON)); err != nil {
			writeErr = err
			return
		}
		for _, sf := range sources {
			// The relative path is the field name (preserved), not the filename
		// (which the receiver would reduce to its base name).
		part, err := mw.CreateFormFile(sf.rel, path.Base(sf.rel))
			if err != nil {
				writeErr = err
				return
			}
			in, err := os.Open(sf.abs)
			if err != nil {
				writeErr = err
				return
			}
			_, copyErr := io.Copy(part, in)
			_ = in.Close()
			if copyErr != nil {
				writeErr = copyErr
				return
			}
		}
		writeErr = mw.Close()
	}()

	req, err := http.NewRequestWithContext(buildCtx, http.MethodPost, base+"/api/programs/build", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := buildClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("forward build to owning node: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("owning node build failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var built struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &built); err != nil || built.ID == "" {
		return "", fmt.Errorf("owning node build returned no program id: %s", strings.TrimSpace(string(body)))
	}
	return built.ID, nil
}

type packedSource struct {
	rel string // {repo}/{relpath}, slash-separated
	abs string
}

// sourceRoot is where the source repos named by a recipe live on this node.
func sourceRoot() string {
	if v := strings.TrimSpace(os.Getenv("MAST_SOURCE_ROOT")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Documents")
}

// packSource collects the files of each named repo under the source root,
// excluding the same build detritus run-on-mac.sh skips, as paths of the form
// {repo}/{relpath} so the repos land side by side on the peer.
// skipSourceDir names build detritus and dependency caches no compiler needs;
// shipping them would balloon the transfer (a Python .venv or node_modules dwarfs
// the source) without helping the build.
func skipSourceDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", ".venv", "venv", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".next")
}

func packSource(repos []string, workdir string) ([]packedSource, error) {
	root := sourceRoot()
	// The repo that holds the build workdir is a monorepo of sibling programs
	// (framekit-programs is ~1 GB of other games' assets and binaries). Only the
	// target program and the repo's shared code are build inputs, so within that
	// repo skip every sibling of the program directory.
	workdir = filepath.ToSlash(strings.Trim(workdir, "/"))
	workdirRepo, programRel := "", ""
	if parts := strings.SplitN(workdir, "/", 2); len(parts) == 2 {
		workdirRepo, programRel = parts[0], parts[1]
	}
	siblingParent := ""
	if programRel != "" {
		if parent := path.Dir(programRel); parent != "." {
			siblingParent = parent
		}
	}

	var files []packedSource
	for _, repo := range repos {
		repoDir := filepath.Join(root, repo)
		info, err := os.Stat(repoDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("source repo %q not found under %s", repo, root)
		}
		isWorkdirRepo := repo == workdirRepo
		walkErr := filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipSourceDir(d.Name()) {
					return filepath.SkipDir
				}
				// Drop sibling program directories in the monorepo — keep only
				// the target program's own directory and its ancestors.
				if isWorkdirRepo && siblingParent != "" {
					repoRel, _ := filepath.Rel(repoDir, p)
					repoRel = filepath.ToSlash(repoRel)
					if path.Dir(repoRel) == siblingParent &&
						repoRel != programRel &&
						!strings.HasPrefix(programRel+"/", repoRel+"/") {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".tar.gz") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, packedSource{rel: filepath.ToSlash(rel), abs: p})
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return files, nil
}
