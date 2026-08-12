package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/brijorn/mast/internal/program"
)

type fakeProgramBackend struct {
	started          program.StartOptions
	resumed          program.ResumeOptions
	stopped          program.StopOptions
	deletedID        string
	autostartID      string
	autostartEnabled bool
	autostartOptions program.AutostartOptions
	logOffsets       program.LogOffsets
	uploaded         program.RegisterUploadOptions
	stopRequested    bool
	stopAcknowledged bool
	artifact         string
	artifactPath     string
	builtRecipe      program.BuildRecipe
	builtSourceCount int
}

func (f *fakeProgramBackend) RequestStop(id string) (*program.Run, error) {
	f.stopRequested = true
	return &program.Run{ID: id, Status: "running"}, nil
}

func (f *fakeProgramBackend) StopRequest(_ string) (*program.StopRequest, error) {
	return &program.StopRequest{}, nil
}

func (f *fakeProgramBackend) AcknowledgeStop(id string) (*program.Run, error) {
	f.stopAcknowledged = true
	return &program.Run{ID: id, Status: "running"}, nil
}

func (f *fakeProgramBackend) ListPrograms() []program.Program {
	return []program.Program{{ID: "sha256-test", Name: "Example"}}
}

func (f *fakeProgramBackend) BuildFromSource(recipe program.BuildRecipe, sources []program.UploadFile) (*program.Program, error) {
	f.builtRecipe = recipe
	f.builtSourceCount = len(sources)
	return &program.Program{ID: "sha256-built", Name: recipe.Name, Slug: recipe.Slug}, nil
}

func (f *fakeProgramBackend) Start(opts program.StartOptions) ([]program.Run, error) {
	f.started = opts
	return []program.Run{{ID: "run-1", ProgramID: opts.ProgramID, Serial: opts.Serials[0], Status: "running"}}, nil
}

func (f *fakeProgramBackend) ListRuns() []program.Run {
	return []program.Run{{ID: "run-1", Status: "running"}}
}

func (f *fakeProgramBackend) RegisterUpload(opts program.RegisterUploadOptions) (*program.Program, error) {
	f.uploaded = opts
	return &program.Program{
		ID:    "sha256-upload",
		Name:  opts.Name,
		Entry: opts.Entry,
	}, nil
}

func (f *fakeProgramBackend) Stop(opts program.StopOptions) (*program.Run, error) {
	f.stopped = opts
	return &program.Run{ID: opts.ID, Status: "running", AutostartPaused: opts.AutostartPaused}, nil
}

func (f *fakeProgramBackend) Logs(_ string) (string, string, error) {
	return "out", "err", nil
}

// artifact is the file OpenArtifact hands back, and artifactPath records what
// the handler asked for, so a test can assert the query reached the store.
func (f *fakeProgramBackend) OpenArtifact(_ string, name string) (io.ReadCloser, os.FileInfo, error) {
	f.artifactPath = name
	if f.artifact == "" {
		return nil, nil, errors.New("artifact not found")
	}
	file, err := os.Open(f.artifact)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func (f *fakeProgramBackend) LogsSince(_ string, offsets program.LogOffsets) (*program.LogsResult, error) {
	f.logOffsets = offsets
	return &program.LogsResult{
		Stdout:       "out",
		Stderr:       "err",
		StdoutOffset: offsets.Stdout + 3,
		StderrOffset: offsets.Stderr + 3,
		StdoutSize:   offsets.Stdout + 3,
		StderrSize:   offsets.Stderr + 3,
	}, nil
}

func (f *fakeProgramBackend) CleanupRun(id string) (*program.Run, error) {
	return &program.Run{ID: id, Status: "exited", WorkspaceCleaned: true}, nil
}

func (f *fakeProgramBackend) Resume(opts program.ResumeOptions) (*program.Run, error) {
	f.resumed = opts
	return &program.Run{ID: opts.ID, Status: "running"}, nil
}

func (f *fakeProgramBackend) SetRunAutostart(id string, enabled bool) (*program.Run, error) {
	f.autostartID = id
	f.autostartEnabled = enabled
	return &program.Run{
		ID: id, Status: "stopped", Autostart: enabled,
		AutostartReconnect: enabled, AutostartCrashRestart: enabled,
	}, nil
}

func (f *fakeProgramBackend) UpdateRunAutostart(id string, opts program.AutostartOptions) (*program.Run, error) {
	f.autostartID = id
	f.autostartOptions = opts
	run := &program.Run{ID: id, Status: "stopped"}
	if opts.Reconnect != nil {
		run.AutostartReconnect = *opts.Reconnect
	}
	if opts.CrashRestart != nil {
		run.AutostartCrashRestart = *opts.CrashRestart
	}
	run.Autostart = run.AutostartReconnect || run.AutostartCrashRestart
	return run, nil
}

func (f *fakeProgramBackend) UpdateProgram(id string, name string, slug string, mappings []program.ConfigMapping) (*program.Program, error) {
	return &program.Program{
		ID:             id,
		Name:           name,
		Slug:           slug,
		ConfigMappings: mappings,
	}, nil
}

func (f *fakeProgramBackend) DeleteProgram(id string) error {
	f.deletedID = id
	return nil
}

func TestStartRunsCallsBackend(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"program_id":"sha256-test","serials":["phone-1"],"variables":{"mode":"normal"},"secret_variables":{"LICENSE_KEY":"abc"}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusCreated, res.Body.String())
	}
	if programs.started.ProgramID != "sha256-test" || programs.started.Serials[0] != "phone-1" {
		t.Fatalf("started = %+v", programs.started)
	}
	if programs.started.SecretVariables["LICENSE_KEY"] != "abc" {
		t.Fatalf("secret variables = %+v", programs.started.SecretVariables)
	}
}

func TestListRunsExposesBothAutostartBehaviors(t *testing.T) {
	server := NewServer(&fakeBackend{}, &fakeProgramBackend{})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	var runs []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&runs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"autostart_reconnect", "autostart_crash_restart"} {
		if _, ok := runs[0][field]; !ok {
			t.Fatalf("run response omitted %q: %+v", field, runs[0])
		}
	}
}

func TestUploadProgramPreservesNestedFilePaths(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("name", "Farkle Dice Roll"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("entry", `{"command":"FarkleBrig.py"}`); err != nil {
		t.Fatal(err)
	}
	mainPart, err := writer.CreateFormFile("files", "FarkleBrig.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mainPart.Write([]byte("print('run')\n")); err != nil {
		t.Fatal(err)
	}
	templatePart, err := writer.CreateFormFile("files", "templates/die_1.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templatePart.Write([]byte("png")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/programs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusCreated, res.Body.String())
	}
	if len(programs.uploaded.Files) != 2 {
		t.Fatalf("uploaded files = %d, want 2", len(programs.uploaded.Files))
	}
	if got := programs.uploaded.Files[1].Path; got != "templates/die_1.png" {
		t.Fatalf("uploaded nested path = %q, want templates/die_1.png", got)
	}
}

func TestResumeRunPassesVariables(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"id":"wrong-id","variables":{"MAX_LEVELS":"30","DEVICE_ID":"phone-1"}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/resume", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if programs.resumed.ID != "run-1" {
		t.Fatalf("resumed ID = %q, want run-1", programs.resumed.ID)
	}
	if programs.resumed.Variables["MAX_LEVELS"] != "30" || programs.resumed.Variables["DEVICE_ID"] != "phone-1" {
		t.Fatalf("variables = %+v", programs.resumed.Variables)
	}
}

func TestStopRunCanPauseAutostart(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"autostart_paused":true}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/stop", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if programs.stopped.ID != "run-1" || !programs.stopped.AutostartPaused {
		t.Fatalf("stopped = %+v, want run-1 with paused autostart", programs.stopped)
	}

	var got program.Run
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.AutostartPaused {
		t.Fatalf("got AutostartPaused = false, want true")
	}
}

func TestSetRunAutostartCallsBackend(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"enabled":true}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/runs/run-1/autostart", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if programs.autostartID != "run-1" || !programs.autostartEnabled {
		t.Fatalf("autostart = id %q enabled %v", programs.autostartID, programs.autostartEnabled)
	}

	var got program.Run
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Autostart {
		t.Fatalf("got Autostart = false, want true")
	}
	if !got.AutostartReconnect || !got.AutostartCrashRestart {
		t.Fatalf("got behavior flags = reconnect %v crash %v, want true/true",
			got.AutostartReconnect, got.AutostartCrashRestart)
	}
}

func TestSetRunAutostartBehaviorsIndependently(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"autostart_reconnect":true,"autostart_crash_restart":false}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/runs/run-1/autostart", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if programs.autostartID != "run-1" ||
		programs.autostartOptions.Reconnect == nil || !*programs.autostartOptions.Reconnect ||
		programs.autostartOptions.CrashRestart == nil || *programs.autostartOptions.CrashRestart {
		t.Fatalf("autostart options = id %q %+v, want reconnect true and crash false",
			programs.autostartID, programs.autostartOptions)
	}

	var got program.Run
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Autostart || !got.AutostartReconnect || got.AutostartCrashRestart {
		t.Fatalf("got flags = autostart %v reconnect %v crash %v, want true/true/false",
			got.Autostart, got.AutostartReconnect, got.AutostartCrashRestart)
	}
}

func TestSoftStopLifecycleEndpointsCallBackend(t *testing.T) {
	backend := &fakeProgramBackend{}
	server := NewServer(nil, backend)
	request := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/stop-request", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !backend.stopRequested {
		t.Fatalf("request response=%d requested=%v body=%s", response.Code, backend.stopRequested, response.Body.String())
	}
	ack := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/stop-ack", nil)
	ackResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(ackResponse, ack)
	if ackResponse.Code != http.StatusOK || !backend.stopAcknowledged {
		t.Fatalf("ack response=%d acknowledged=%v body=%s", ackResponse.Code, backend.stopAcknowledged, ackResponse.Body.String())
	}
}

func TestRunLogsReturnsOutput(t *testing.T) {
	server := NewServer(&fakeBackend{}, &fakeProgramBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/logs", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}

	var got runLogsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Stdout != "out" || got.Stderr != "err" {
		t.Fatalf("logs = %+v", got)
	}
	if got.StdoutOffset != 3 || got.StderrOffset != 3 {
		t.Fatalf("offsets = stdout %d stderr %d, want 3/3", got.StdoutOffset, got.StderrOffset)
	}
}

func TestRunLogsPassesOffsets(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/logs?stdout_offset=10&stderr_offset=20", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if programs.logOffsets.Stdout != 10 || programs.logOffsets.Stderr != 20 {
		t.Fatalf("offsets = %+v, want stdout 10 stderr 20", programs.logOffsets)
	}

	var got runLogsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.StdoutOffset != 13 || got.StderrOffset != 23 {
		t.Fatalf("response offsets = stdout %d stderr %d, want 13/23", got.StdoutOffset, got.StderrOffset)
	}
}

func TestUpdateProgramCallsBackend(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"config_mappings":[{"section":"Settings","key":"DEVICE_ID","value":"{{phone.serial}}"}]}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/programs/test-id", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}

	var got program.Program
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "test-id" {
		t.Fatalf("got ID = %q, want test-id", got.ID)
	}
	if len(got.ConfigMappings) != 1 || got.ConfigMappings[0].Value != "{{phone.serial}}" {
		t.Fatalf("got ConfigMappings = %+v", got.ConfigMappings)
	}
}

func TestDeleteProgramCallsBackend(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/programs/test-id", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if programs.deletedID != "test-id" {
		t.Fatalf("deletedID = %q, want test-id", programs.deletedID)
	}
}

// The route is the gateway to a run's evidence, so it must pass the requested
// path to the store that owns the containment check, and must not invent a
// success when there is no such file.
func TestRunArtifactServesTheStoresFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(path, []byte("frame-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	programs := &fakeProgramBackend{artifact: path}
	server := NewServer(nil, programs)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/runs/run-1/artifact?path=debug%2Fframe.png", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "frame-bytes" {
		t.Fatalf("body = %q", got)
	}
	if programs.artifactPath != "debug/frame.png" {
		t.Fatalf("store asked for %q", programs.artifactPath)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
}

func TestRunArtifactIsNotFoundWhenTheStoreRefuses(t *testing.T) {
	server := NewServer(nil, &fakeProgramBackend{})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/runs/run-1/artifact?path=../../etc/passwd", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}
