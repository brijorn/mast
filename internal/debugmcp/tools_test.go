package debugmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brijorn/mast/internal/program"
)

func TestListProgramsAndRunsUseMockAPI(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/programs":
			writeJSON(t, w, []program.Program{{
				ID:    "sha256-abc",
				Slug:  "my-script",
				Name:  "My Script",
				Entry: program.Entry{Command: "main.py"},
			}})
		case "/api/runs":
			writeJSON(t, w, []program.Run{
				{ID: "run-1", ProgramSlug: "my-script", Serial: "device-123", Status: "running"},
				{ID: "run-2", ProgramSlug: "other-script", Serial: "device-999", Status: "stopped"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	s := testServer(api.URL)
	programs := callToolJSON[[]program.Program](t, s, "list_programs", nil)
	if len(programs) != 1 || programs[0].Slug != "my-script" {
		t.Fatalf("unexpected programs: %#v", programs)
	}

	runs := callToolJSON[[]program.Run](t, s, "list_runs", map[string]any{
		"serial":       "device-123",
		"program_slug": "my-script",
		"status":       "running",
	})
	if len(runs) != 1 || runs[0].ID != "run-1" {
		t.Fatalf("unexpected filtered runs: %#v", runs)
	}
}

func TestGetRunLogsSendsOffsets(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/run-1/logs" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("stdout_offset"); got != "10" {
			t.Fatalf("stdout_offset = %q", got)
		}
		if got := r.URL.Query().Get("stderr_offset"); got != "20" {
			t.Fatalf("stderr_offset = %q", got)
		}
		writeJSON(t, w, map[string]any{"stdout": "ok", "stderr": "", "stdout_offset": 12, "stderr_offset": 20})
	}))
	defer api.Close()

	logs := callToolJSON[map[string]any](t, testServer(api.URL), "get_run_logs", map[string]any{
		"run_id":        "run-1",
		"stdout_offset": 10,
		"stderr_offset": 20,
	})
	if logs["stdout"] != "ok" {
		t.Fatalf("unexpected logs: %#v", logs)
	}
}

func TestCaptureScreenshotWritesCallerPath(t *testing.T) {
	pngBytes := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/devices/device-123/screenshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer api.Close()

	out := filepath.Join(t.TempDir(), "screen.png")
	result := callToolJSON[map[string]any](t, testServer(api.URL), "capture_screenshot", map[string]any{
		"serial":      "device-123",
		"output_path": out,
	})
	if result["path"] != out {
		t.Fatalf("path = %#v", result["path"])
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pngBytes) {
		t.Fatalf("screenshot bytes = %v", got)
	}
}

func TestStartProgramPrefersSlugAndRejectsExe(t *testing.T) {
	var startedBody map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/programs":
			writeJSON(t, w, []program.Program{
				{ID: "sha256-current", Slug: "my-script", Entry: program.Entry{Command: "main.py"}},
				{ID: "sha256-exe", Slug: "windows-tool", Entry: program.Entry{Command: "tool.exe"}},
			})
		case "/api/runs":
			if err := json.NewDecoder(r.Body).Decode(&startedBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, w, []program.Run{{ID: "run-1", ProgramID: "sha256-current", ProgramSlug: "my-script"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	s := testServer(api.URL)
	callToolJSON[[]program.Run](t, s, "start_program", map[string]any{
		"program_id": "sha256-current",
		"serials":    []any{"device-123"},
	})
	if startedBody["program_id"] != "my-script" {
		t.Fatalf("start used %q, want slug", startedBody["program_id"])
	}

	errResult := s.CallTool(context.Background(), "start_program", map[string]any{
		"slug":    "windows-tool",
		"serials": []any{"device-123"},
	})
	if !errResult.IsError || !strings.Contains(errResult.Content[0].Text, ".exe") {
		t.Fatalf("expected exe rejection, got %#v", errResult)
	}
}

func TestStopAndResumeRun(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/run-1/stop":
			writeJSON(t, w, program.Run{ID: "run-1", Status: "stopped"})
		case "/api/runs/run-1/resume":
			var req program.ResumeOptions
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Variables["MAX_LEVELS"] != "30" {
				t.Fatalf("variables = %#v", req.Variables)
			}
			writeJSON(t, w, program.Run{ID: "run-1", Status: "running"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	s := testServer(api.URL)
	stopped := callToolJSON[program.Run](t, s, "stop_run", map[string]any{"run_id": "run-1"})
	if stopped.Status != "stopped" {
		t.Fatalf("stopped status = %q", stopped.Status)
	}
	resumed := callToolJSON[program.Run](t, s, "resume_run", map[string]any{
		"run_id":    "run-1",
		"variables": map[string]any{"MAX_LEVELS": "30"},
	})
	if resumed.Status != "running" {
		t.Fatalf("resumed status = %q", resumed.Status)
	}
}

func TestUploadScriptBundleMultipart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "__pycache__"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__pycache__", "main.pyc"), []byte("ignore"), 0600); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/programs/upload" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("name") != "My Script" {
			t.Fatalf("name = %q", r.FormValue("name"))
		}
		var entry program.Entry
		if err := json.Unmarshal([]byte(r.FormValue("entry")), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Command != "main.py" {
			t.Fatalf("entry = %#v", entry)
		}
		files := r.MultipartForm.File["files"]
		if len(files) != 1 || files[0].Filename != "main.py" {
			t.Fatalf("files = %#v", files)
		}
		writeJSON(t, w, program.Program{ID: "sha256-new", Slug: "my-script", Entry: entry})
	}))
	defer api.Close()

	uploaded := callToolJSON[program.Program](t, testServer(api.URL), "upload_script_bundle", map[string]any{
		"path":  dir,
		"name":  "My Script",
		"entry": map[string]any{"command": "main.py"},
	})
	if uploaded.ID != "sha256-new" {
		t.Fatalf("uploaded = %#v", uploaded)
	}
}

func TestGetCurrentBundleUsesAPIConfig(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/programs":
			writeJSON(t, w, []program.Program{{ID: "sha256-current", Slug: "my-script", Entry: program.Entry{Command: "main.py"}}})
		case "/api/nodes":
			writeJSON(t, w, []map[string]any{{"id": "node-a", "local": true}})
		case "/api/nodes/node-a/config":
			writeJSON(t, w, map[string]any{"programs_dir": "/tmp/mast-debug/programs"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	result := callToolJSON[map[string]any](t, testServer(api.URL), "get_current_bundle", map[string]any{"slug": "my-script"})
	if result["bundle_path"] != "/tmp/mast-debug/programs/bundles/sha256-current" {
		t.Fatalf("bundle path = %#v", result["bundle_path"])
	}
}

func TestTemplateMatchSyntheticImages(t *testing.T) {
	dir := t.TempDir()
	screenPath := filepath.Join(dir, "screen.png")
	templatePath := filepath.Join(dir, "template.png")

	screen := image.NewRGBA(image.Rect(0, 0, 80, 60))
	fill(screen, color.RGBA{R: 245, G: 245, B: 245, A: 255})
	template := image.NewRGBA(image.Rect(0, 0, 12, 10))
	fill(template, color.RGBA{R: 230, G: 90, B: 25, A: 255})
	drawRect(screen, image.Rect(31, 22, 43, 32), color.RGBA{R: 230, G: 90, B: 25, A: 255})
	if err := writePNG(screenPath, screen); err != nil {
		t.Fatal(err)
	}
	if err := writePNG(templatePath, template); err != nil {
		t.Fatal(err)
	}

	result := callToolJSON[templateMatchResult](t, testServer("http://127.0.0.1:1"), "template_match", map[string]any{
		"screenshot_path": screenPath,
		"template_path":   templatePath,
		"threshold":       0.99,
	})
	if !result.Matched || result.TopLeft.X != 31 || result.TopLeft.Y != 22 {
		t.Fatalf("unexpected match: %#v", result)
	}
}

func testServer(baseURL string) *Server {
	return NewServer(Config{
		BaseURL:     baseURL,
		AllowedExts: map[string]bool{".py": true, ".js": true, ".sh": true},
	})
}

func callToolJSON[T any](t *testing.T, s *Server, name string, args map[string]any) T {
	t.Helper()
	result := s.CallTool(context.Background(), name, args)
	if result.IsError {
		t.Fatalf("%s failed: %s", name, result.Content[0].Text)
	}
	if len(result.Content) != 1 {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
	var out T
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func fill(img *image.RGBA, c color.RGBA) {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}
