package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/program"
)

type fakeProgramBackend struct {
	registered program.RegisterOptions
	started    program.StartOptions
}

func (f *fakeProgramBackend) Register(opts program.RegisterOptions) (*program.Program, error) {
	f.registered = opts
	return &program.Program{
		ID:        "sha256-test",
		Name:      opts.Name,
		Entry:     opts.Entry,
		CreatedAt: time.Now().UTC(),
	}, nil
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
	return &program.Program{
		ID:       "sha256-upload",
		Name:     opts.Name,
		Entry:    opts.Entry,
	}, nil
}

func (f *fakeProgramBackend) Stop(id string) (*program.Run, error) {
	return &program.Run{ID: id, Status: "running"}, nil
}

func (f *fakeProgramBackend) Logs(_ string) (string, string, error) {
	return "out", "err", nil
}

func (f *fakeProgramBackend) CleanupRun(id string) (*program.Run, error) {
	return &program.Run{ID: id, Status: "exited", WorkspaceCleaned: true}, nil
}

func TestRegisterProgramCallsBackend(t *testing.T) {
	programs := &fakeProgramBackend{}
	server := NewServer(&fakeBackend{}, programs)

	body := []byte(`{"path":"/tmp/example","name":"Example","platform":"windows","entry":{"command":"app.exe"},"ini_values":[{"section":"Settings","key":"DEVICE_ID","value":"{{phone.serial}}"}]}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/programs", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusCreated, res.Body.String())
	}
	if programs.registered.Path != "/tmp/example" || programs.registered.Entry.Command != "app.exe" {
		t.Fatalf("registered = %+v", programs.registered)
	}

	var got program.Program
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "sha256-test" {
		t.Fatalf("ID = %q, want sha256-test", got.ID)
	}
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
}
