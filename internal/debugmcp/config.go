package debugmcp

import (
	"os"
	"strings"
)

const defaultMastAPIURL = "http://127.0.0.1:6271"

type Config struct {
	BaseURL           string
	AllowedExts       map[string]bool
	AllowExe          bool
	ScreenshotBaseDir string
}

func ConfigFromEnv() Config {
	baseURL := strings.TrimSpace(os.Getenv("MAST_API_URL"))
	if baseURL == "" {
		baseURL = defaultMastAPIURL
	}

	return Config{
		BaseURL:           baseURL,
		AllowedExts:       allowedExtsFromEnv(),
		AllowExe:          envBool("MAST_DEBUG_MCP_ALLOW_EXE"),
		ScreenshotBaseDir: strings.TrimSpace(os.Getenv("MAST_DEBUG_MCP_SCREENSHOT_DIR")),
	}
}

func allowedExtsFromEnv() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("MAST_DEBUG_MCP_ALLOWED_EXTENSIONS"))
	if raw == "" {
		raw = ".py,.js,.sh,.bash,.zsh"
	}
	allowed := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		ext := strings.ToLower(strings.TrimSpace(item))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		allowed[ext] = true
	}
	return allowed
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
