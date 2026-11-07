// Package config loads Baton's configuration: a global file (~/.baton/config.yaml) plus an
// optional per-project override (<repo>/baton.config.yaml). It validates without ever
// failing hard (errors are reported, the last good config stays in effect), reloads on change
// by polling, and writes edits back to the right file preserving comments.
//
// Types and signatures are the Phase 2 contract; the implementation lives in load.go,
// watch.go and write.go. Field names = YAML keys (snake_case).
package config

import "time"

// Modes for how agent lineups are decided.
const (
	ModeUser    = "user"    // the human specifies everything
	ModePropose = "propose" // conductor proposes, human approves (default)
	ModeAuto    = "auto"    // conductor decides within caps and reports why
)

// Worktree cleanup policies.
const (
	CleanupKeep          = "keep" // default
	CleanupRemoveOnMerge = "remove_on_merge"
	CleanupRemoveAfter   = "remove_after_days" // uses Worktrees.Days
)

// Config is the effective (merged, defaulted) configuration.
type Config struct {
	Hub       Hub                   `yaml:"hub" json:"hub"`
	Conductor Conductor             `yaml:"conductor" json:"conductor"`
	Accounts  []Account             `yaml:"accounts" json:"accounts"`
	Routing   map[string]string     `yaml:"routing" json:"routing"` // role -> account id
	Kinds     map[string]CustomKind `yaml:"kinds" json:"kinds"`     // custom adapter kinds (Phase 5)
}

// Hub holds hub-wide settings. Defaults in brackets.
type Hub struct {
	Port              int       `yaml:"port" json:"port"`                               // [7777] preferred; falls back to next free
	Bind              string    `yaml:"bind" json:"bind"`                               // [127.0.0.1] loopback only
	HeartbeatTimeoutS int       `yaml:"heartbeat_timeout_s" json:"heartbeat_timeout_s"` // [120] -> stalled
	AskTimeoutS       int       `yaml:"ask_timeout_s" json:"ask_timeout_s"`             // [900] baton ask --blocking
	PollIntervalS     float64   `yaml:"poll_interval_s" json:"poll_interval_s"`         // [2] baton ask polling
	IdleStopMin       int       `yaml:"idle_stop_min" json:"idle_stop_min"`             // [30] 0 = never stop when idle
	OpenBrowser       bool      `yaml:"open_browser" json:"open_browser"`               // [true] `baton` opens the dashboard
	Mode              string    `yaml:"mode" json:"mode"`                               // [propose] user | propose | auto
	MaxAgentsAuto     int       `yaml:"max_agents_auto" json:"max_agents_auto"`         // [6] global cap, auto mode only
	RunsKeepDays      int       `yaml:"runs_keep_days" json:"runs_keep_days"`           // [30] `baton gc` removes ended run folders older than this; 0 = never
	Worktrees         Worktrees `yaml:"worktrees" json:"worktrees"`
	Approvals         Approvals `yaml:"approvals" json:"approvals"`
}

// Worktrees is the cleanup policy for agent worktrees.
type Worktrees struct {
	Cleanup string `yaml:"cleanup" json:"cleanup"` // [keep] keep | remove_on_merge | remove_after_days
	Days    int    `yaml:"days" json:"days"`       // for remove_after_days [14]
}

// Approvals are human approval gates (defaults to be confirmed in Phase 4).
type Approvals struct {
	Merge bool `yaml:"merge" json:"merge"` // [true] merges need a human click
	Spawn bool `yaml:"spawn" json:"spawn"` // [false] spawns need a human click (propose mode asks anyway)
}

// Conductor selects the conductor's account, model and effort (any model; Phase 6).
type Conductor struct {
	Account  string `yaml:"account" json:"account"`     // "" = the first enabled claude account
	Model    string `yaml:"model" json:"model"`         // [opus] e.g. opus, sonnet — whatever the account's CLI accepts
	Effort   string `yaml:"effort" json:"effort"`       // [high] CLI-specific effort level, "" = CLI default
	AutoWake bool   `yaml:"auto_wake" json:"auto_wake"` // [true] nudge the idle conductor when agents need it
}

// Account is one CLI login (or API-key provider) agents can run on.
type Account struct {
	ID                       string            `yaml:"id" json:"id"`
	Label                    string            `yaml:"label" json:"label"`
	Kind                     string            `yaml:"kind" json:"kind"` // claude | codex | opencode | generic | <custom kind>
	Color                    string            `yaml:"color" json:"color"`
	Env                      map[string]string `yaml:"env" json:"env"` // per-account isolation, e.g. CLAUDE_CONFIG_DIR (~ expanded)
	Models                   []string          `yaml:"models" json:"models"`
	Roles                    []string          `yaml:"roles" json:"roles"`
	MaxConcurrent            int               `yaml:"max_concurrent" json:"max_concurrent"` // [1]
	Enabled                  bool              `yaml:"enabled" json:"enabled"`               // [true]
	ForbidClaudeSubscription bool              `yaml:"forbid_claude_subscription" json:"forbid_claude_subscription"`
	Permissions              Permissions       `yaml:"permissions" json:"permissions"`
	// Optional per-account overrides of hub timeouts (0 = use the hub value).
	HeartbeatTimeoutS int     `yaml:"heartbeat_timeout_s" json:"heartbeat_timeout_s"`
	AskTimeoutS       int     `yaml:"ask_timeout_s" json:"ask_timeout_s"`
	PollIntervalS     float64 `yaml:"poll_interval_s" json:"poll_interval_s"`
}

// Permissions are the worker permission settings. Workers run unattended. "sandboxed" (the
// default) runs them in the CLI's OS sandbox: writes only to their worktree and run folder,
// no reading of Baton's home, no network. "permissive" turns all checks off; "default" and
// "acceptEdits" use the CLI's own prompts, which nobody can answer in a headless run.
type Permissions struct {
	Mode         string   `yaml:"mode" json:"mode"`           // [sandboxed] sandboxed | permissive | acceptEdits | default
	DenyRead     []string `yaml:"deny_read" json:"deny_read"` // sandboxed: extra paths agents may not read (~ expanded); Baton's home is always denied
	Ask          bool     `yaml:"ask" json:"ask"`             // [true] sandboxed codex: ask the user (dashboard) when the sandbox blocks something; false = just refuse
	AllowedTools []string `yaml:"allowed_tools" json:"allowed_tools"`
	DeniedTools  []string `yaml:"denied_tools" json:"denied_tools"`
	ExtraArgs    []string `yaml:"extra_args" json:"extra_args"`
}

// CustomKind defines an adapter kind entirely in config (Phase 5).
type CustomKind struct {
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"env"`
	Parser  string            `yaml:"parser" json:"parser"` // none | stream-json | jsonl
	Notes   string            `yaml:"notes" json:"notes"`   // kind notes for the brief
}

// Issue is one validation or parse problem, located as precisely as possible.
type Issue struct {
	File string `json:"file"`           // which file ("" = merged result)
	Path string `json:"path"`           // YAML path, e.g. accounts[1].max_concurrent
	Line int    `json:"line,omitempty"` // 1-based, 0 = unknown
	Msg  string `json:"msg"`
}

// Snapshot is one loaded configuration. Config is always usable: when the files are invalid
// it is the last good config (or the defaults), and Issues says what is wrong.
type Snapshot struct {
	Config      Config    `json:"config"`
	Issues      []Issue   `json:"issues"`
	GlobalPath  string    `json:"global_path"`
	ProjectPath string    `json:"project_path"`
	LoadedAt    time.Time `json:"loaded_at"`
	Version     int64     `json:"version"` // increments on every successful (re)load
}

// Account returns the account with the given id.
func (c *Config) Account(id string) (*Account, bool) {
	for i := range c.Accounts {
		if c.Accounts[i].ID == id {
			return &c.Accounts[i], true
		}
	}
	return nil, false
}
