package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/brijorn/mast/internal/program"
)

// maxUploadSize caps directory uploads at 200 MB.
const maxUploadSize = 200 << 20

type runLogsResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
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
	configFile := r.FormValue("config_file")

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

	// Open all file parts; close them all after RegisterUpload returns.
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	uploadFiles := make([]program.UploadFile, 0, len(fileHeaders))
	for _, fh := range fileHeaders {
		f, err := fh.Open()
		if err != nil {
			http.Error(w, "opening upload part: "+err.Error(), http.StatusInternalServerError)
			return
		}
		closers = append(closers, f)
		uploadFiles = append(uploadFiles, program.UploadFile{
			Path:    fh.Filename,
			Content: f,
		})
	}

	registered, err := s.programs.RegisterUpload(program.RegisterUploadOptions{
		Name:           name,
		ConfigFile:     configFile,
		ConfigMappings: configMappings,
		Entry:          entry,
		Files:          uploadFiles,
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

func (s *Server) ListRuns(w http.ResponseWriter, _ *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.programs.ListRuns()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) StopRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	run, err := s.programs.Stop(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ResumeRun handles POST /api/runs/{id}/resume.
// It re-executes a stopped, failed, exited, or lost run in its existing workspace.
func (s *Server) ResumeRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	run, err := s.programs.Resume(r.PathValue("id"))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
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

	stdout, stderr, err := s.programs.Logs(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(runLogsResponse{Stdout: stdout, Stderr: stderr}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) UpdateProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	var req struct {
		ConfigMappings []program.ConfigMapping `json:"config_mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	updated, err := s.programs.UpdateProgram(id, req.ConfigMappings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(updated); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
