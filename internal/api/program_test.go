package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brijorn/mast/internal/program"
)

type fakeProgramBackend struct {
	started          program.StartOptions
	resumed          program.ResumeOptions
	deletedID        string
	autostartID      string
	autostartEnabled bool
	logOffsets       program.LogOffsets
	uploaded         program.RegisterUploadOptions
}

func (f *fakeProgramBackend) ListPrograms() []program.Program {
	return []program.Program{{ID: "sha256-test", Name: "Example"}}
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

func (f *fakeProgramBackend) Stop(id string) (*program.Run, error) {
	return &program.Run{ID: id, Status: "running"}, nil
}

func (f *fakeProgramBackend) Logs(_ string) (string, string, error) {
	return "out", "err", nil
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
	return &program.Run{ID: id, Status: "stopped", Autostart: enabled}, nil
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

	body := []byte(`{"program_id":"sha256-test","serials":["phone-1"],"variables":{"license_key":"abc"}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusCreated, res.Body.String())
	}
	if programs.started.ProgramID != "sha256-test" || programs.started.Serials[0] != "phone-1" {
		t.Fatalf("started = %+v", programs.started)
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
