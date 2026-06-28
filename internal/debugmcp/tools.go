package debugmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brijorn/mast/internal/program"
)

type uploadBundleArgs struct {
	Path           string                  `json:"path"`
	Name           string                  `json:"name,omitempty"`
	Entry          program.Entry           `json:"entry"`
	ConfigFile     string                  `json:"config_file,omitempty"`
	ConfigMappings []program.ConfigMapping `json:"config_mappings,omitempty"`
}

func (s *Server) toolDefinitions() []map[string]any {
	return []map[string]any{
		toolDef("list_programs", "List current Mast programs.", map[string]any{}),
		toolDef("list_runs", "List Mast program runs, with optional local filtering.", map[string]any{
			"serial":       strSchema("Only return runs for this device serial."),
			"program_slug": strSchema("Only return runs for this program slug."),
			"status":       strSchema("Only return runs in this status."),
		}),
		toolDef("get_run_logs", "Fetch stdout and stderr for a run.", map[string]any{
			"run_id":        requiredStr("Run ID."),
			"stdout_offset": intSchema("Optional stdout byte offset for incremental polling."),
			"stderr_offset": intSchema("Optional stderr byte offset for incremental polling."),
		}, "run_id"),
		toolDef("capture_screenshot", "Capture a device screenshot to an explicit caller-provided path.", map[string]any{
			"serial":      requiredStr("Device serial."),
			"output_path": strSchema("Where to write the screenshot. Required unless MAST_DEBUG_MCP_SCREENSHOT_DIR is set."),
		}, "serial"),
		toolDef("start_program", "Start the latest script program by slug when possible.", map[string]any{
			"slug":       strSchema("Program slug. Preferred over program_id."),
			"program_id": strSchema("Program content ID. Used only when slug is absent."),
			"serials":    arraySchema("Device serials to start on."),
			"variables":  objectSchema("Run variables."),
		}, "serials"),
		toolDef("stop_run", "Stop a program run.", map[string]any{
			"run_id": requiredStr("Run ID."),
		}, "run_id"),
		toolDef("resume_run", "Resume a stopped, failed, exited, or lost run.", map[string]any{
			"run_id":    requiredStr("Run ID."),
			"variables": objectSchema("Optional variables to apply to this resumed attempt."),
		}, "run_id"),
		toolDef("upload_script_bundle", "Upload a directory as a script bundle.", map[string]any{
			"path":            requiredStr("Directory containing the bundle."),
			"name":            strSchema("Program name."),
			"entry":           objectSchema("Entry object, for example {\"command\":\"main.py\",\"args\":[]}."),
			"config_file":     strSchema("Optional config file path inside the bundle."),
			"config_mappings": arraySchema("Optional config mappings."),
		}, "path", "entry"),
		toolDef("get_current_bundle", "Return current program metadata and local bundle path when Mast exposes the local programs directory.", map[string]any{
			"slug":       strSchema("Program slug."),
			"program_id": strSchema("Program content ID."),
		}),
		toolDef("template_match", "Find a template image inside a screenshot image.", map[string]any{
			"screenshot_path": requiredStr("Path to the screenshot PNG/JPEG."),
			"template_path":   requiredStr("Path to the template PNG/JPEG."),
			"threshold":       numSchema("Match threshold from 0 to 1. Defaults to 0.92."),
		}, "screenshot_path", "template_path"),
	}
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "list_programs":
		return s.client.listPrograms(ctx)
	case "list_runs":
		return s.listRuns(ctx, args)
	case "get_run_logs":
		return s.getRunLogs(ctx, args)
	case "capture_screenshot":
		return s.captureScreenshot(ctx, args)
	case "start_program":
		return s.startProgram(ctx, args)
	case "stop_run":
		return s.stopRun(ctx, args)
	case "resume_run":
		return s.resumeRun(ctx, args)
	case "upload_script_bundle":
		return s.uploadScriptBundle(ctx, args)
	case "get_current_bundle":
		return s.getCurrentBundle(ctx, args)
	case "template_match":
		return s.templateMatch(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *Server) listRuns(ctx context.Context, args map[string]any) (any, error) {
	runs, err := s.client.listRuns(ctx)
	if err != nil {
		return nil, err
	}
	serial := stringArg(args, "serial")
	slug := firstStringArg(args, "program_slug", "slug")
	status := stringArg(args, "status")
	filtered := runs[:0]
	for _, run := range runs {
		if serial != "" && run.Serial != serial {
			continue
		}
		if slug != "" && run.ProgramSlug != slug {
			continue
		}
		if status != "" && run.Status != status {
			continue
		}
		filtered = append(filtered, run)
	}
	return filtered, nil
}

func (s *Server) getRunLogs(ctx context.Context, args map[string]any) (any, error) {
	id := stringArg(args, "run_id")
	if id == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	return s.client.getRunLogs(ctx, id, int64PtrArg(args, "stdout_offset"), int64PtrArg(args, "stderr_offset"))
}

func (s *Server) captureScreenshot(ctx context.Context, args map[string]any) (any, error) {
	serial := stringArg(args, "serial")
	if serial == "" {
		return nil, fmt.Errorf("serial is required")
	}
	outputPath := stringArg(args, "output_path")
	if outputPath == "" && s.config.ScreenshotBaseDir != "" {
		outputPath = filepath.Join(s.config.ScreenshotBaseDir, serial+".png")
	}
	if outputPath == "" {
		return nil, fmt.Errorf("output_path is required unless MAST_DEBUG_MCP_SCREENSHOT_DIR is set")
	}

	body, contentType, err := s.client.screenshot(ctx, serial)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(outputPath, body, 0600); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":         outputPath,
		"bytes":        len(body),
		"content_type": contentType,
	}, nil
}

func (s *Server) startProgram(ctx context.Context, args map[string]any) (any, error) {
	serials := stringSliceArg(args, "serials")
	if len(serials) == 0 {
		return nil, fmt.Errorf("serials is required")
	}
	target := firstStringArg(args, "slug", "program_slug")
	if target == "" {
		target = stringArg(args, "program_id")
	}
	if target == "" {
		return nil, fmt.Errorf("slug or program_id is required")
	}

	programs, err := s.client.listPrograms(ctx)
	if err != nil {
		return nil, err
	}
	match, ok := findProgram(programs, target)
	if !ok {
		return nil, fmt.Errorf("program %q not found; start_program only starts known script programs", target)
	}
	if err := s.validateEntry(match.Entry); err != nil {
		return nil, err
	}

	startID := match.Slug
	if startID == "" {
		startID = match.ID
	}
	return s.client.startProgram(ctx, startID, serials, stringMapArg(args, "variables"))
}

func (s *Server) stopRun(ctx context.Context, args map[string]any) (any, error) {
	id := stringArg(args, "run_id")
	if id == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	return s.client.stopRun(ctx, id)
}

func (s *Server) resumeRun(ctx context.Context, args map[string]any) (any, error) {
	id := stringArg(args, "run_id")
	if id == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	return s.client.resumeRun(ctx, id, stringMapArg(args, "variables"))
}

func (s *Server) uploadScriptBundle(ctx context.Context, args map[string]any) (any, error) {
	var req uploadBundleArgs
	if err := decodeArgs(args, &req); err != nil {
		return nil, err
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if req.Entry.Command == "" {
		return nil, fmt.Errorf("entry.command is required")
	}
	if err := s.validateEntry(req.Entry); err != nil {
		return nil, err
	}
	return s.client.uploadScriptBundle(ctx, req)
}

func (s *Server) getCurrentBundle(ctx context.Context, args map[string]any) (any, error) {
	target := firstStringArg(args, "slug", "program_slug", "program_id")
	if target == "" {
		return nil, fmt.Errorf("slug or program_id is required")
	}
	programs, err := s.client.listPrograms(ctx)
	if err != nil {
		return nil, err
	}
	match, ok := findProgram(programs, target)
	if !ok {
		return nil, fmt.Errorf("program %q not found", target)
	}

	response := map[string]any{"program": match}
	dir, err := s.client.localProgramsDir(ctx)
	if err != nil {
		response["bundle_path_warning"] = err.Error()
		return response, nil
	}
	response["bundle_path"] = filepath.Join(dir, "bundles", match.ID)
	return response, nil
}

func (s *Server) templateMatch(args map[string]any) (any, error) {
	screenshotPath := stringArg(args, "screenshot_path")
	templatePath := stringArg(args, "template_path")
	if screenshotPath == "" {
		return nil, fmt.Errorf("screenshot_path is required")
	}
	if templatePath == "" {
		return nil, fmt.Errorf("template_path is required")
	}
	return templateMatch(screenshotPath, templatePath, floatArg(args, "threshold"))
}

func (s *Server) validateEntry(entry program.Entry) error {
	command := strings.TrimSpace(entry.Command)
	if command == "" {
		return fmt.Errorf("entry.command is required")
	}
	ext := strings.ToLower(filepath.Ext(command))
	if ext == ".exe" && !s.config.AllowExe {
		return fmt.Errorf("entry command %q is an .exe; set MAST_DEBUG_MCP_ALLOW_EXE=true to allow it", command)
	}
	if ext == "" {
		return fmt.Errorf("entry command %q has no extension; allowed script extensions are %s", command, s.allowedExtList())
	}
	if !s.config.AllowedExts[ext] {
		return fmt.Errorf("entry command %q uses extension %q; allowed script extensions are %s", command, ext, s.allowedExtList())
	}
	return nil
}

func (s *Server) allowedExtList() string {
	items := make([]string, 0, len(s.config.AllowedExts))
	for ext := range s.config.AllowedExts {
		items = append(items, ext)
	}
	sort.Strings(items)
	return strings.Join(items, ", ")
}

func findProgram(programs []program.Program, target string) (program.Program, bool) {
	for _, p := range programs {
		if p.Slug == target || p.ID == target {
			return p, true
		}
	}
	return program.Program{}, false
}

func decodeArgs(args map[string]any, out any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func firstStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringArg(args, key); value != "" {
			return value
		}
	}
	return ""
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
		return out
	default:
		return nil
	}
}

func stringMapArg(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	out := map[string]string{}
	switch values := raw.(type) {
	case map[string]string:
		for k, v := range values {
			out[k] = v
		}
	case map[string]any:
		for k, v := range values {
			if str, ok := v.(string); ok {
				out[k] = str
			} else {
				out[k] = fmt.Sprint(v)
			}
		}
	}
	return out
}

func int64PtrArg(args map[string]any, key string) *int64 {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case float64:
		v := int64(value)
		return &v
	case int64:
		return &value
	case int:
		v := int64(value)
		return &v
	default:
		return nil
	}
}

func floatArg(args map[string]any, key string) float64 {
	raw, ok := args[key]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return 0
	}
}

func toolDef(name, description string, props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

func requiredStr(description string) map[string]any {
	s := strSchema(description)
	s["minLength"] = 1
	return s
}

func strSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func numSchema(description string) map[string]any {
	return map[string]any{"type": "number", "description": description, "minimum": 0, "maximum": 1}
}

func objectSchema(description string) map[string]any {
	return map[string]any{"type": "object", "description": description, "additionalProperties": true}
}

func arraySchema(description string) map[string]any {
	return map[string]any{"type": "array", "description": description}
}
