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
	Command    string           `json:"command"`
	Args       []string         `json:"args,omitempty"`
	Companions []CompanionEntry `json:"companions,omitempty"`
}

type CompanionCondition struct {
	Variable string `json:"variable"`
	Equals   string `json:"equals"`
}

type CompanionEntry struct {
	ID          string             `json:"id"`
	Command     string             `json:"command"`
	Args        []string           `json:"args,omitempty"`
	EnabledWhen CompanionCondition `json:"enabled_when"`
	Required    bool               `json:"required,omitempty"`
}

type RunProcess struct {
	ID       string   `json:"id"`
	Cmd      string   `json:"cmd"`
	CmdArgs  []string `json:"cmd_args,omitempty"`
	Required bool     `json:"required,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Error    string   `json:"error,omitempty"`
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
	// FinishesOnCleanExit says a zero exit from this program means it completed
	// the work it was given, so there is nothing for a crash restart to recover.
	// Programs that end for their own reasons and expect to be started again —
	// a licensed executable that closes after a session — leave it false and
	// keep being resumed whenever they end on their own.
	FinishesOnCleanExit bool      `json:"finishes_on_clean_exit,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type AutostartSupervisorState struct {
	RestartAttempts int        `json:"restart_attempts"`
	Abandoned       bool       `json:"abandoned"`
	LastError       string     `json:"last_error,omitempty"`
	LastFailureAt   *time.Time `json:"last_failure_at,omitempty"`
	NextRestartAt   *time.Time `json:"next_restart_at,omitempty"`
}

type Run struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	ID            string `json:"id"`
	ProgramID     string `json:"program_id"`
	Serial        string `json:"serial"`
	NodeID        string `json:"node_id"`
	Workspace     string `json:"workspace"`
	Status        string `json:"status"`
	// Autostart is the legacy aggregate. It is true when either independently
	// controllable automatic recovery behavior is enabled.
	Autostart             bool              `json:"autostart,omitempty"`
	AutostartReconnect    bool              `json:"autostart_reconnect"`
	AutostartCrashRestart bool              `json:"autostart_crash_restart"`
	AutostartPaused       bool              `json:"autostart_paused,omitempty"`
	ExitCode              *int              `json:"exit_code,omitempty"`
	Error                 string            `json:"error,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	// Cmd and CmdArgs are the resolved command and arguments used to start this
	// run. They are persisted so that Resume can re-execute the same process.
	Cmd        string       `json:"cmd,omitempty"`
	CmdArgs    []string     `json:"cmd_args,omitempty"`
	Companions []RunProcess `json:"companions,omitempty"`
	PID        int          `json:"pid,omitempty"`
	// PIDStartTime is when the kernel started PID, in the clock ticks since
	// boot that /proc reports. It is the run's claim on that number: a PID is
	// only reused by a process that started later, so a start time that still
	// matches is proof this is the same process and not a stranger wearing its
	// PID.
	//
	// The process itself is what has to be recognized, and nothing it does to
	// its own environment may change the answer. Identifying it by working
	// directory instead cost a run its life: Wine moves into the wineserver's
	// socket directory while it starts that server, and a run checked inside
	// those few hundred milliseconds looked like a stranger and was declared
	// lost while it was running perfectly well.
	PIDStartTime       uint64     `json:"pid_start_time,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	StopRequestedAt    *time.Time `json:"stop_requested_at,omitempty"`
	StopAcknowledgedAt *time.Time `json:"stop_acknowledged_at,omitempty"`
	// CheckpointPolledAt is the last time this run's program asked whether a
	// stop is pending. Only a program built on FrameKit's checkpoint loop ever
	// asks, so its presence is the proof that a soft stop can be honored at
	// all: a licensed executable never polls, and waiting out a grace period
	// for one is time spent on an acknowledgement that cannot arrive.
	//
	// It is live observation of a running process rather than run state, so it
	// is filled in on the listing snapshot and never written to run.json — a
	// value restored from disk would claim a capability for a process this Mast
	// has never watched.
	CheckpointPolledAt *time.Time `json:"checkpoint_polled_at,omitempty"`
	// WorkspaceCleaned is true after the run's workspace directory has been
	// removed by CleanupRun.
	WorkspaceCleaned bool  `json:"workspace_cleaned,omitempty"`
	StdoutLogStart   int64 `json:"stdout_log_start,omitempty"`
	StderrLogStart   int64 `json:"stderr_log_start,omitempty"`
	// AutostartSupervisor is the durable crash-restart incident state exposed
	// to API clients. It remains present after give-up until an explicit resume,
	// crash-restart disable, or a failure-free recovery window clears it.
	AutostartSupervisor *AutostartSupervisorState `json:"autostart_supervisor,omitempty"`
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
	Name                string
	Slug                string
	ConfigFile          string
	ConfigMappings      []ConfigMapping
	Entry               Entry
	FinishesOnCleanExit bool
	Files               []UploadFile
}

type StartOptions struct {
	ProgramID       string            `json:"program_id"`
	Serials         []string          `json:"serials"`
	Variables       map[string]string `json:"variables,omitempty"`
	SecretVariables map[string]string `json:"secret_variables,omitempty"`
}

type ResumeOptions struct {
	ID              string            `json:"id,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	SecretVariables map[string]string `json:"secret_variables,omitempty"`
	Supervisor      bool              `json:"-"`
}

type StopOptions struct {
	ID              string `json:"id,omitempty"`
	AutostartPaused bool   `json:"autostart_paused,omitempty"`
}

type AutostartOptions struct {
	Reconnect    *bool `json:"autostart_reconnect,omitempty"`
	CrashRestart *bool `json:"autostart_crash_restart,omitempty"`
}

type StopRequest struct {
	RequestedAt    *time.Time `json:"requested_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
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
