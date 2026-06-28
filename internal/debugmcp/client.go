package debugmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/node"
	"github.com/brijorn/mast/internal/program"
)

type mastClient struct {
	baseURL string
	http    *http.Client
}

func newMastClient(baseURL string) *mastClient {
	return &mastClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *mastClient) listPrograms(ctx context.Context) ([]program.Program, error) {
	var programs []program.Program
	if err := c.doJSON(ctx, http.MethodGet, "/api/programs", nil, &programs); err != nil {
		return nil, err
	}
	return programs, nil
}

func (c *mastClient) listRuns(ctx context.Context) ([]program.Run, error) {
	var runs []program.Run
	if err := c.doJSON(ctx, http.MethodGet, "/api/runs", nil, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (c *mastClient) getRunLogs(ctx context.Context, id string, stdoutOffset, stderrOffset *int64) (map[string]any, error) {
	path := "/api/runs/" + url.PathEscape(id) + "/logs"
	query := url.Values{}
	if stdoutOffset != nil {
		query.Set("stdout_offset", fmt.Sprintf("%d", *stdoutOffset))
	}
	if stderrOffset != nil {
		query.Set("stderr_offset", fmt.Sprintf("%d", *stderrOffset))
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var logs map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}

func (c *mastClient) screenshot(ctx context.Context, serial string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/devices/"+url.PathEscape(serial)+"/screenshot", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, strings.TrimSpace(string(body)))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func (c *mastClient) startProgram(ctx context.Context, programID string, serials []string, variables map[string]string) ([]program.Run, error) {
	req := program.StartOptions{
		ProgramID: programID,
		Serials:   serials,
		Variables: variables,
	}
	var runs []program.Run
	if err := c.doJSON(ctx, http.MethodPost, "/api/runs", req, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (c *mastClient) stopRun(ctx context.Context, id string) (program.Run, error) {
	var run program.Run
	if err := c.doJSON(ctx, http.MethodPost, "/api/runs/"+url.PathEscape(id)+"/stop", map[string]any{}, &run); err != nil {
		return program.Run{}, err
	}
	return run, nil
}

func (c *mastClient) resumeRun(ctx context.Context, id string, variables map[string]string) (program.Run, error) {
	req := program.ResumeOptions{Variables: variables}
	var run program.Run
	if err := c.doJSON(ctx, http.MethodPost, "/api/runs/"+url.PathEscape(id)+"/resume", req, &run); err != nil {
		return program.Run{}, err
	}
	return run, nil
}

func (c *mastClient) uploadScriptBundle(ctx context.Context, req uploadBundleArgs) (program.Program, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if req.Name != "" {
		if err := writer.WriteField("name", req.Name); err != nil {
			return program.Program{}, err
		}
	}
	entry, err := json.Marshal(req.Entry)
	if err != nil {
		return program.Program{}, err
	}
	if err := writer.WriteField("entry", string(entry)); err != nil {
		return program.Program{}, err
	}
	if req.ConfigFile != "" {
		if err := writer.WriteField("config_file", req.ConfigFile); err != nil {
			return program.Program{}, err
		}
	}
	if len(req.ConfigMappings) > 0 {
		mappings, err := json.Marshal(req.ConfigMappings)
		if err != nil {
			return program.Program{}, err
		}
		if err := writer.WriteField("config_mappings", string(mappings)); err != nil {
			return program.Program{}, err
		}
	}

	files, err := bundleFiles(req.Path)
	if err != nil {
		return program.Program{}, err
	}
	if len(files) == 0 {
		return program.Program{}, fmt.Errorf("bundle path contains no uploadable files")
	}
	for _, rel := range files {
		full := filepath.Join(req.Path, rel)
		part, err := writer.CreateFormFile("files", filepath.ToSlash(rel))
		if err != nil {
			return program.Program{}, err
		}
		f, err := os.Open(full)
		if err != nil {
			return program.Program{}, err
		}
		if _, err := io.Copy(part, f); err != nil {
			_ = f.Close()
			return program.Program{}, err
		}
		if err := f.Close(); err != nil {
			return program.Program{}, err
		}
	}

	if err := writer.Close(); err != nil {
		return program.Program{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/programs/upload", &body)
	if err != nil {
		return program.Program{}, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return program.Program{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return program.Program{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return program.Program{}, fmt.Errorf("%s %s: %s", httpReq.Method, httpReq.URL.Path, strings.TrimSpace(string(respBody)))
	}

	var uploaded program.Program
	if err := json.Unmarshal(respBody, &uploaded); err != nil {
		return program.Program{}, err
	}
	return uploaded, nil
}

func (c *mastClient) localProgramsDir(ctx context.Context) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("MAST_PROGRAMS_DIR")); dir != "" {
		return dir, nil
	}

	var nodes []node.NodeInfo
	if err := c.doJSON(ctx, http.MethodGet, "/api/nodes", nil, &nodes); err != nil {
		return "", err
	}
	for _, n := range nodes {
		if !n.Local {
			continue
		}
		var cfg mastconfig.Config
		if err := c.doJSON(ctx, http.MethodGet, "/api/nodes/"+url.PathEscape(n.ID)+"/config", nil, &cfg); err != nil {
			return "", err
		}
		if cfg.ProgramsDir == "" {
			return "", fmt.Errorf("local node config did not include programs_dir")
		}
		return cfg.ProgramsDir, nil
	}
	return "", fmt.Errorf("local Mast node not found")
}

func (c *mastClient) doJSON(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(respBody)))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func bundleFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bundle path must be a directory")
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && skippedBundleDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if skippedBundleFile(name) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func skippedBundleDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "node_modules", ".venv", "venv":
		return true
	default:
		return false
	}
}

func skippedBundleFile(name string) bool {
	switch name {
	case ".DS_Store":
		return true
	default:
		return strings.HasSuffix(name, ".pyc")
	}
}
