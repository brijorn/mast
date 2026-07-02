package program

import (
	"io"
	"time"
)

const (
	RegistryFileName = "registry.json"
	DefaultADBPort   = 5037
	runLogMaxBytes   = 10 << 20

	RunStatusStarting = "starting"
	RunStatusRunning  = "running"
	RunStatusExited   = "exited"
	RunStatusFailed   = "failed"
	RunStatusStopped  = "stopped"
	RunStatusLost     = "lost"
)

type Entry struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type ConfigMapping struct {
	Section string `json:"section,omitempty"`
	Key     string `json:"key,omitempty"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type Program struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug,omitempty"`
	Name           string          `json:"name"`
	ConfigFile     string          `json:"config_file,omitempty"`
	ConfigMappings []ConfigMapping `json:"config_mappings,omitempty"`
	Entry          Entry           `json:"entry"`
	CreatedAt      time.Time       `json:"created_at"`
}

type Run struct {
	ID              string            `json:"id"`
	ProgramID       string            `json:"program_id"`
	Serial          string            `json:"serial"`
	NodeID          string            `json:"node_id"`
	Workspace       string            `json:"workspace"`
	Status          string            `json:"status"`
	Autostart       bool              `json:"autostart,omitempty"`
	AutostartPaused bool              `json:"autostart_paused,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Error           string            `json:"error,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	// Cmd and CmdArgs are the resolved command and arguments used to start this
	// run. They are persisted so that Resume can re-execute the same process.
	Cmd         string     `json:"cmd,omitempty"`
	CmdArgs     []string   `json:"cmd_args,omitempty"`
	PID         int        `json:"pid,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// WorkspaceCleaned is true after the run's workspace directory has been
	// removed by CleanupRun.
	WorkspaceCleaned bool  `json:"workspace_cleaned,omitempty"`
	StdoutLogStart   int64 `json:"stdout_log_start,omitempty"`
	StderrLogStart   int64 `json:"stderr_log_start,omitempty"`
}

// UploadFile is a single file within a directory upload.
// Path is the relative path inside the program bundle (e.g. "config.ini").
type UploadFile struct {
	Path    string
	Content io.Reader
	Open    func() (io.ReadCloser, error)
}

// RegisterUploadOptions describes a program bundle uploaded as individual files.
type RegisterUploadOptions struct {
	Name           string
	Slug           string
	ConfigFile     string
	ConfigMappings []ConfigMapping
	Entry          Entry
	Files          []UploadFile
}

type StartOptions struct {
	ProgramID string            `json:"program_id"`
	Serials   []string          `json:"serials"`
	Variables map[string]string `json:"variables,omitempty"`
}

type ResumeOptions struct {
	ID        string            `json:"id,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
}

type StopOptions struct {
	ID              string `json:"id,omitempty"`
	AutostartPaused bool   `json:"autostart_paused,omitempty"`
}

type LogOffsets struct {
	Stdout int64
	Stderr int64
}

type LogsResult struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	StdoutOffset int64  `json:"stdout_offset"`
	StderrOffset int64  `json:"stderr_offset"`
	StdoutSize   int64  `json:"stdout_size"`
	StderrSize   int64  `json:"stderr_size"`
	StdoutReset  bool   `json:"stdout_reset,omitempty"`
	StderrReset  bool   `json:"stderr_reset,omitempty"`
}

type registryFile struct {
	Programs []Program `json:"programs"`
}
