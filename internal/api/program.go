package api

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brijorn/mast/internal/program"
)

// maxUploadSize caps directory uploads at 200 MB.
const maxUploadSize = 200 << 20

type runLogsResponse struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	StdoutOffset int64  `json:"stdout_offset"`
	StderrOffset int64  `json:"stderr_offset"`
	StdoutSize   int64  `json:"stdout_size"`
	StderrSize   int64  `json:"stderr_size"`
	StdoutReset  bool   `json:"stdout_reset,omitempty"`
	StderrReset  bool   `json:"stderr_reset,omitempty"`
}

// UploadProgram handles POST /api/programs/upload.
// It accepts a multipart/form-data body with the following fields:
//
//   - name        – program name (optional; defaults to "unnamed")
//   - platform    – target OS (optional; inferred from entry command extension)
//   - entry       – JSON-encoded Entry object, e.g. {"command":"run.sh"}
//   - ini_values  – JSON-encoded []INIValue (optional)
//   - files       – one or more file parts; the filename of each part is the
//     relative path within the bundle (e.g. "config.ini", "data/seed.db")
func (s *Server) UploadProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parsing upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	slug := r.FormValue("slug")
	configFile := r.FormValue("config_file")
	finishesOnCleanExit := r.FormValue("finishes_on_clean_exit") == "true"

	var entry program.Entry
	if entryStr := r.FormValue("entry"); entryStr != "" {
		if err := json.Unmarshal([]byte(entryStr), &entry); err != nil {
			http.Error(w, "invalid entry: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var configMappings []program.ConfigMapping
	if mappingsStr := r.FormValue("config_mappings"); mappingsStr != "" {
		if err := json.Unmarshal([]byte(mappingsStr), &configMappings); err != nil {
			http.Error(w, "invalid config_mappings: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	fileHeaders := r.MultipartForm.File["files"]
	if len(fileHeaders) == 0 {
		http.Error(w, "at least one file required", http.StatusBadRequest)
		return
	}

	uploadFiles := make([]program.UploadFile, 0, len(fileHeaders))
	for _, fh := range fileHeaders {
		fh := fh
		uploadFiles = append(uploadFiles, program.UploadFile{
			Path: uploadFilePath(fh),
			Open: func() (io.ReadCloser, error) {
				return fh.Open()
			},
		})
	}

	registered, err := s.programs.RegisterUpload(program.RegisterUploadOptions{
		Name:           name,
		Slug:           slug,
		ConfigFile:     configFile,
		ConfigMappings: configMappings,
		Entry:          entry,

		FinishesOnCleanExit: finishesOnCleanExit,
		Files:               uploadFiles,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(registered); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func uploadFilePath(fh *multipart.FileHeader) string {
	path := fh.Filename
	if _, params, err := mime.ParseMediaType(fh.Header.Get("Content-Disposition")); err == nil {
		if raw := strings.TrimSpace(params["filename"]); raw != "" {
			path = raw
		}
	}
	return strings.ReplaceAll(path, "\\", "/")
}

func (s *Server) DeleteProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	if err := s.programs.DeleteProgram(r.PathValue("id")); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListPrograms(w http.ResponseWriter, _ *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.programs.ListPrograms()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) StartRuns(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	var req program.StartOptions
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Run on the node that owns the device when asked. A start already forwarded
	// to us (proxy marker) is not forwarded again — the owning peer runs it
	// locally, so on that hop the owner resolves as local and this is skipped.
	if req.RunOnOwningNode && r.Header.Get(proxyMarker) == "" && len(req.Serials) > 0 {
		peerBase, err := s.resolveSingleOwnerPeerBase(req.Serials)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if peerBase != "" {
			// Build the native bundle on the peer first when a recipe is given,
			// then start the built program there. gocv can't cross-compile, so
			// the peer that owns the phone is the only host that can build it.
			if req.Build != nil {
				builtID, err := s.buildOnPeer(r.Context(), peerBase, *req.Build)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				req.ProgramID = builtID
			}
			req.Build = nil
			if s.forwardStartToNode(w, r, peerBase, req) {
				return
			}
		}
	}

	runs, err := s.programs.Start(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(runs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ListRuns(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	// A peer aggregating our runs asks with local=1 so we answer with only the
	// runs we own; without that guard two aggregating nodes would query each
	// other forever.
	if isTruthy(r.URL.Query().Get("local")) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(s.programs.ListRuns()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Local runs plus every peer's, so an operator monitoring through one node
	// sees runs executing on the nodes that own their phones too.
	local := make([]json.RawMessage, 0)
	for _, run := range s.programs.ListRuns() {
		if encoded, err := json.Marshal(run); err == nil {
			local = append(local, encoded)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.mergedRuns(local)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) StopRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	req := program.StopOptions{ID: r.PathValue("id")}
	if body := readBodyPreserving(r); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	req.ID = r.PathValue("id")

	run, err := s.programs.Stop(req)
	if err != nil {
		if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/stop") {
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) RequestRunStop(w http.ResponseWriter, r *http.Request) {
	programs, ok := s.programs.(runStopRequester)
	if !ok {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	run, err := programs.RequestStop(r.PathValue("id"))
	if err != nil {
		if strings.Contains(err.Error(), "not found") &&
			s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/stop-request") {
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

func (s *Server) GetRunStopRequest(w http.ResponseWriter, r *http.Request) {
	programs, ok := s.programs.(runStopRequester)
	if !ok {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	request, err := programs.StopRequest(r.PathValue("id"))
	if err != nil {
		if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/stop-request") {
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(request)
}

func (s *Server) AcknowledgeRunStop(w http.ResponseWriter, r *http.Request) {
	programs, ok := s.programs.(runStopRequester)
	if !ok {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	run, err := programs.AcknowledgeStop(r.PathValue("id"))
	if err != nil {
		if strings.Contains(err.Error(), "not found") &&
			s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/stop-ack") {
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

// ResumeRun handles POST /api/runs/{id}/resume.
// It re-executes a stopped, failed, exited, or lost run in its existing workspace.
func (s *Server) ResumeRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	req := program.ResumeOptions{ID: r.PathValue("id")}
	if body := readBodyPreserving(r); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	req.ID = r.PathValue("id")

	run, err := s.programs.Resume(req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/resume") {
				return
			}
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) SetRunAutostart(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Enabled      *bool `json:"enabled"`
		Reconnect    *bool `json:"autostart_reconnect"`
		CrashRestart *bool `json:"autostart_crash_restart"`
	}
	if err := json.Unmarshal(readBodyPreserving(r), &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Enabled != nil && (req.Reconnect != nil || req.CrashRestart != nil) {
		http.Error(w, "enabled cannot be combined with behavior-specific fields", http.StatusBadRequest)
		return
	}

	var (
		run *program.Run
		err error
	)
	if req.Enabled != nil {
		run, err = s.programs.SetRunAutostart(r.PathValue("id"), *req.Enabled)
	} else {
		run, err = s.programs.UpdateRunAutostart(r.PathValue("id"), program.AutostartOptions{
			Reconnect:    req.Reconnect,
			CrashRestart: req.CrashRestart,
		})
	}
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/autostart") {
				return
			}
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// CleanupRun handles POST /api/runs/{id}/cleanup.
// It removes the workspace directory of a completed or failed run.
func (s *Server) CleanupRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	run, err := s.programs.CleanupRun(r.PathValue("id"))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/cleanup") {
				return
			}
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) RunLogs(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	offsets, err := parseLogOffsets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logs, err := s.programs.LogsSince(r.PathValue("id"), offsets)
	if err != nil {
		// Not in this node's store — the run may be executing on a peer.
		if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/logs") {
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(runLogsResponse{
		Stdout:       logs.Stdout,
		Stderr:       logs.Stderr,
		StdoutOffset: logs.StdoutOffset,
		StderrOffset: logs.StderrOffset,
		StdoutSize:   logs.StdoutSize,
		StderrSize:   logs.StderrSize,
		StdoutReset:  logs.StdoutReset,
		StderrReset:  logs.StderrReset,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func parseLogOffsets(r *http.Request) (program.LogOffsets, error) {
	query := r.URL.Query()
	stdout, err := parseOptionalOffset(query.Get("stdout_offset"))
	if err != nil {
		return program.LogOffsets{}, err
	}
	stderr, err := parseOptionalOffset(query.Get("stderr_offset"))
	if err != nil {
		return program.LogOffsets{}, err
	}
	return program.LogOffsets{Stdout: stdout, Stderr: stderr}, nil
}

func parseOptionalOffset(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, nil
	}
	return offset, nil
}

func (s *Server) UpdateProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	var req struct {
		Name           string                  `json:"name"`
		Slug           string                  `json:"slug"`
		ConfigMappings []program.ConfigMapping `json:"config_mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	updated, err := s.programs.UpdateProgram(id, req.Name, req.Slug, req.ConfigMappings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(updated); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// RunArtifact handles GET /api/runs/{id}/artifact?path=…
//
// A run reports evidence by absolute path, which only the node holding the
// workspace can open. Serving it here is what lets a log row show the frame it
// is describing instead of a file name the reader has to go and find.
func (s *Server) RunArtifact(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	file, info, err := s.programs.OpenArtifact(r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")+"/artifact") {
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()

	contentType := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(info.Name())) {
	case ".png":
		contentType = "image/png"
	case ".jpg", ".jpeg":
		contentType = "image/jpeg"
	case ".webp":
		contentType = "image/webp"
	case ".json":
		contentType = "application/json"
	case ".txt", ".log":
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	// Evidence is written once under a timestamped name and never rewritten,
	// so a fetched frame can be held for as long as the reader keeps the panel
	// open rather than re-pulled on every poll.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(info.Name()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file.(io.ReadSeeker))
}
