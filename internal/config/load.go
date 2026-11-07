package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/whosabhiraj/baton/internal/paths"
)

const (
	unknownKeyPrefix = "unknown key"
	forcedPrefix     = "forced:"
	notePrefix       = "note:" // advisory: valid, but it will not work the way it reads here
)

var (
	idRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	lineRe  = regexp.MustCompile(`line (\d+): (.*)`)
)

// Warning reports whether the issue is advisory (unknown key, forced value). Warnings never
// stop a config from loading.
func (i Issue) Warning() bool {
	return strings.HasPrefix(i.Msg, unknownKeyPrefix) || strings.HasPrefix(i.Msg, forcedPrefix) || strings.HasPrefix(i.Msg, notePrefix)
}

// OK reports whether the snapshot has no error-level issues.
func (s *Snapshot) OK() bool { return countErrors(s.Issues) == 0 }

func countErrors(is []Issue) int {
	n := 0
	for _, i := range is {
		if !i.Warning() {
			n++
		}
	}
	return n
}

// Defaults returns the built-in configuration (no accounts).
func Defaults() Config {
	return Config{
		Hub: Hub{
			Port: 7777, Bind: "127.0.0.1", HeartbeatTimeoutS: 120, AskTimeoutS: 900,
			PollIntervalS: 2, IdleStopMin: 30, OpenBrowser: true, Mode: ModePropose,
			MaxAgentsAuto: 6, RunsKeepDays: 30,
			Worktrees: Worktrees{Cleanup: CleanupKeep, Days: 14},
			Approvals: Approvals{Merge: true, Spawn: false},
		},
		Conductor: Conductor{Model: "opus", Effort: "high", AutoWake: true},
		Accounts:  []Account{},
		Routing:   map[string]string{},
		Kinds:     map[string]CustomKind{},
	}
}

// ModeSandboxed is the default worker permission mode (see Permissions).
const ModeSandboxed = "sandboxed"

// hostOS is runtime.GOOS; a variable so tests can check other platforms' advice.
var hostOS = runtime.GOOS

func defaultAccount() Account {
	return Account{MaxConcurrent: 1, Enabled: true, Permissions: Permissions{Mode: ModeSandboxed}}
}

func defaultKind() CustomKind { return CustomKind{Parser: "none"} }

// GlobalPath is <BATON_HOME>/config.yaml.
func GlobalPath() string { return filepath.Join(paths.BatonHome(), "config.yaml") }

// ProjectPath is <repo>/baton.config.yaml.
func ProjectPath(repoRoot string) string { return filepath.Join(repoRoot, "baton.config.yaml") }

// source is one config file read into memory.
type source struct {
	path    string
	data    []byte
	present bool
	err     error
}

func readSource(path string) source {
	s := source{path: path}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		s.data, s.present = b, true
	case errors.Is(err, os.ErrNotExist):
	default:
		s.err = err
	}
	return s
}

// Load reads, merges (project overrides global; accounts merged by id, field by field) and
// validates both files. Missing files are fine (defaults). Never returns a nil Snapshot.
// When the files have errors, Config is Defaults() and Issues says what is wrong (the
// Watcher keeps the last good config instead).
func Load(globalPath, projectPath string) *Snapshot {
	return build(readSource(globalPath), readSource(projectPath))
}

type builder struct {
	issues []Issue
	origin map[*yaml.Node]string
}

func (b *builder) add(file string, n *yaml.Node, path, msg string) {
	line := 0
	if n != nil {
		line = n.Line
	}
	b.issues = append(b.issues, Issue{File: file, Path: path, Line: line, Msg: msg})
}

func (b *builder) addAt(root *yaml.Node, path []any, msg string) {
	n := lookup(root, path...)
	b.add(b.origin[n], n, pathStr(path), msg)
}

func (b *builder) mark(n *yaml.Node, file string) {
	if n == nil {
		return
	}
	b.origin[n] = file
	for _, c := range n.Content {
		b.mark(c, file)
	}
}

// parse parses one source into its root mapping node (nil if absent/empty/invalid).
func (b *builder) parse(s source) *yaml.Node {
	if s.err != nil {
		b.add(s.path, nil, "", "cannot read file: "+s.err.Error())
		return nil
	}
	if !s.present {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(s.data, &doc); err != nil {
		b.yamlErr(s.path, err)
		return nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return nil
	}
	if root.Kind != yaml.MappingNode {
		b.add(s.path, root, "", "top level must be a mapping")
		return nil
	}
	b.mark(root, s.path)
	b.checkUnknown(s.path, root, reflect.TypeOf(Config{}), "")
	return root
}

func (b *builder) yamlErr(file string, err error) {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, m := range te.Errors {
			b.issues = append(b.issues, typeIssue(file, m))
		}
		return
	}
	b.issues = append(b.issues, typeIssue(file, strings.TrimPrefix(err.Error(), "yaml: ")))
}

func typeIssue(file, m string) Issue {
	is := Issue{File: file, Msg: m}
	if g := lineRe.FindStringSubmatch(m); g != nil {
		is.Line, _ = strconv.Atoi(g[1])
		is.Msg = g[2]
	}
	return is
}

func build(g, p source) *Snapshot {
	b := &builder{origin: map[*yaml.Node]string{}}
	snap := &Snapshot{GlobalPath: g.path, ProjectPath: p.path, LoadedAt: time.Now()}
	gr, pr := b.parse(g), b.parse(p)
	cfg := Defaults()
	if merged := mergeRoots(gr, pr); merged != nil && countErrors(b.issues) == 0 {
		b.decode(merged, &cfg)
		b.validate(merged, &cfg)
	}
	if countErrors(b.issues) == 0 {
		snap.Config = cfg
		snap.Version = 1
	} else {
		snap.Config = Defaults()
	}
	snap.Issues = b.issues
	if snap.Issues == nil {
		snap.Issues = []Issue{}
	}
	return snap
}

// ---- merge ----

func mergeRoots(g, p *yaml.Node) *yaml.Node {
	switch {
	case g == nil:
		return p
	case p == nil:
		return g
	}
	return mergeMaps(g, p, true)
}

func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// mergeMaps overlays b onto a: mappings merge recursively, everything else is replaced.
// Null values in b do not override.
func mergeMaps(a, b *yaml.Node, top bool) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: a.Line, Column: a.Column}
	out.Content = append(out.Content, a.Content...)
	for i := 0; i+1 < len(b.Content); i += 2 {
		k, v := b.Content[i], b.Content[i+1]
		if isNull(v) {
			continue
		}
		j := keyIndex(out, k.Value)
		if j < 0 {
			out.Content = append(out.Content, k, v)
			continue
		}
		cur := out.Content[j+1]
		switch {
		case top && k.Value == "accounts" && cur.Kind == yaml.SequenceNode && v.Kind == yaml.SequenceNode:
			out.Content[j+1] = mergeAccounts(cur, v)
		case cur.Kind == yaml.MappingNode && v.Kind == yaml.MappingNode:
			out.Content[j+1] = mergeMaps(cur, v, false)
		default:
			out.Content[j+1] = v
		}
	}
	return out
}

func accountID(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.MappingNode {
		return "", false
	}
	if i := keyIndex(n, "id"); i >= 0 && n.Content[i+1].Kind == yaml.ScalarNode {
		return n.Content[i+1].Value, true
	}
	return "", false
}

func mergeAccounts(g, p *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Line: g.Line, Column: g.Column}
	out.Content = append(out.Content, g.Content...)
	n0 := len(g.Content)
	for _, pa := range p.Content {
		id, ok := accountID(pa)
		hit := -1
		if ok {
			for i := 0; i < n0; i++ {
				if gid, ok2 := accountID(out.Content[i]); ok2 && gid == id {
					hit = i
					break
				}
			}
		}
		if hit >= 0 && out.Content[hit].Kind == yaml.MappingNode && pa.Kind == yaml.MappingNode {
			out.Content[hit] = mergeMaps(out.Content[hit], pa, false)
		} else {
			out.Content = append(out.Content, pa)
		}
	}
	return out
}

// ---- decode ----

func (b *builder) decodeInto(root *yaml.Node, path []any, n *yaml.Node, dst any) {
	err := n.Decode(dst)
	if err == nil {
		return
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, m := range te.Errors {
			is := typeIssue(b.origin[n], m)
			is.Path = pathStr(path)
			b.issues = append(b.issues, is)
		}
		return
	}
	b.addAt(root, path, err.Error())
}

func (b *builder) decode(root *yaml.Node, cfg *Config) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		if isNull(v) {
			continue
		}
		switch k {
		case "hub":
			b.decodeInto(root, []any{"hub"}, v, &cfg.Hub)
		case "conductor":
			b.decodeInto(root, []any{"conductor"}, v, &cfg.Conductor)
		case "routing":
			b.decodeInto(root, []any{"routing"}, v, &cfg.Routing)
		case "accounts":
			if v.Kind != yaml.SequenceNode {
				b.addAt(root, []any{"accounts"}, "must be a list")
				continue
			}
			for idx, an := range v.Content {
				a := defaultAccount()
				b.decodeInto(root, []any{"accounts", idx}, an, &a)
				if a.Kind == "opencode" && lookupExact(an, "forbid_claude_subscription") == nil {
					a.ForbidClaudeSubscription = true
				}
				cfg.Accounts = append(cfg.Accounts, a)
			}
		case "kinds":
			if v.Kind != yaml.MappingNode {
				b.addAt(root, []any{"kinds"}, "must be a mapping")
				continue
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				name := v.Content[j].Value
				ck := defaultKind()
				b.decodeInto(root, []any{"kinds", name}, v.Content[j+1], &ck)
				cfg.Kinds[name] = ck
			}
		}
	}
	if cfg.Routing == nil {
		cfg.Routing = map[string]string{}
	}
	home, _ := os.UserHomeDir()
	for i := range cfg.Accounts {
		expandEnv(cfg.Accounts[i].Env, home)
		for j, p := range cfg.Accounts[i].Permissions.DenyRead {
			cfg.Accounts[i].Permissions.DenyRead[j] = expandHome(p, home)
		}
	}
	for _, k := range cfg.Kinds {
		expandEnv(k.Env, home)
	}
}

func lookupExact(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	if i := keyIndex(m, key); i >= 0 {
		return m.Content[i+1]
	}
	return nil
}

// expandEnv expands a leading ~ to the home directory; nothing else is expanded.
func expandEnv(env map[string]string, home string) {
	for k, v := range env {
		env[k] = expandHome(v, home)
	}
}

func expandHome(v, home string) string {
	switch {
	case home == "":
		return v
	case v == "~":
		return home
	case strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`):
		return filepath.Join(home, v[2:])
	}
	return v
}

// ---- paths / lookup ----

func pathStr(segs []any) string {
	var sb strings.Builder
	for _, s := range segs {
		switch v := s.(type) {
		case int:
			fmt.Fprintf(&sb, "[%d]", v)
		case string:
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.WriteString(v)
		}
	}
	return sb.String()
}

// lookup walks path (string keys / int indexes) and returns the deepest node that exists.
func lookup(root *yaml.Node, path ...any) *yaml.Node {
	cur := root
	for _, s := range path {
		var next *yaml.Node
		switch v := s.(type) {
		case string:
			next = lookupExact(cur, v)
		case int:
			if cur.Kind == yaml.SequenceNode && v >= 0 && v < len(cur.Content) {
				next = cur.Content[v]
			}
		}
		if next == nil {
			return cur
		}
		cur = next
	}
	return cur
}

// ---- unknown keys ----

func yamlTags(t reflect.Type) map[string]reflect.Type {
	m := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if tag != "" && tag != "-" {
			m[tag] = t.Field(i).Type
		}
	}
	return m
}

func (b *builder) checkUnknown(file string, n *yaml.Node, t reflect.Type, path string) {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return
		}
		tags := yamlTags(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			ft, ok := tags[k.Value]
			if !ok {
				b.add(file, k, join(k.Value), unknownKeyPrefix+" "+strconv.Quote(k.Value))
				continue
			}
			b.checkUnknown(file, v, ft, join(k.Value))
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode || t.Elem().Kind() != reflect.Struct {
			return
		}
		for i, c := range n.Content {
			b.checkUnknown(file, c, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode || t.Elem().Kind() != reflect.Struct {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			b.checkUnknown(file, n.Content[i+1], t.Elem(), join(n.Content[i].Value))
		}
	}
}

// ---- validation ----

func oneOf(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

func isLoopback(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (b *builder) validate(root *yaml.Node, cfg *Config) {
	bad := func(msg string, path ...any) { b.addAt(root, path, msg) }
	h := cfg.Hub
	if h.Port < 0 || h.Port > 65535 {
		bad("port must be 0..65535", "hub", "port")
	}
	if !isLoopback(h.Bind) {
		bad("bind must be a loopback address (127.0.0.1, ::1 or localhost)", "hub", "bind")
	}
	if h.HeartbeatTimeoutS <= 0 {
		bad("must be > 0", "hub", "heartbeat_timeout_s")
	}
	if h.AskTimeoutS <= 0 {
		bad("must be > 0", "hub", "ask_timeout_s")
	}
	if h.PollIntervalS <= 0 {
		bad("must be > 0", "hub", "poll_interval_s")
	}
	if h.IdleStopMin < 0 {
		bad("must be >= 0", "hub", "idle_stop_min")
	}
	if h.RunsKeepDays < 0 {
		bad("must be >= 0", "hub", "runs_keep_days")
	}
	if h.MaxAgentsAuto < 0 {
		bad("must be >= 0", "hub", "max_agents_auto")
	}
	if !oneOf(h.Mode, ModeUser, ModePropose, ModeAuto) {
		bad("mode must be user, propose or auto", "hub", "mode")
	}
	if !oneOf(h.Worktrees.Cleanup, CleanupKeep, CleanupRemoveOnMerge, CleanupRemoveAfter) {
		bad("cleanup must be keep, remove_on_merge or remove_after_days", "hub", "worktrees", "cleanup")
	}
	if h.Worktrees.Days < 1 {
		bad("days must be >= 1", "hub", "worktrees", "days")
	}

	ids := map[string]int{}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if !idRe.MatchString(a.ID) {
			bad("id must match "+idRe.String(), "accounts", i, "id")
		} else if first, dup := ids[a.ID]; dup {
			bad(fmt.Sprintf("duplicate account id %q (first at accounts[%d])", a.ID, first), "accounts", i, "id")
		} else {
			ids[a.ID] = i
		}
		if !oneOf(a.Kind, "claude", "codex", "opencode", "generic") {
			if _, ok := cfg.Kinds[a.Kind]; !ok {
				bad(fmt.Sprintf("kind %q is not claude, codex, opencode, generic or a key under kinds", a.Kind), "accounts", i, "kind")
			}
		}
		if a.Color != "" && !colorRe.MatchString(a.Color) {
			bad("color must be #rrggbb", "accounts", i, "color")
		}
		if a.MaxConcurrent < 1 {
			bad("max_concurrent must be >= 1", "accounts", i, "max_concurrent")
		}
		if !oneOf(a.Permissions.Mode, ModeSandboxed, "permissive", "acceptEdits", "default") {
			bad("mode must be sandboxed, permissive, acceptEdits or default", "accounts", i, "permissions", "mode")
		} else if a.Permissions.Mode == ModeSandboxed && a.Kind == "claude" && hostOS == "windows" {
			bad(notePrefix+" claude agents cannot be sandboxed on native Windows, so this account's agents will refuse to start; set permissions.mode: permissive to run them unsandboxed", "accounts", i, "permissions", "mode")
		}
		if a.HeartbeatTimeoutS < 0 {
			bad("must be >= 0", "accounts", i, "heartbeat_timeout_s")
		}
		if a.AskTimeoutS < 0 {
			bad("must be >= 0", "accounts", i, "ask_timeout_s")
		}
		if a.PollIntervalS < 0 {
			bad("must be >= 0", "accounts", i, "poll_interval_s")
		}
		if a.Kind == "opencode" && !a.ForbidClaudeSubscription {
			a.ForbidClaudeSubscription = true
			bad(forcedPrefix+" opencode accounts always have forbid_claude_subscription: true", "accounts", i, "forbid_claude_subscription")
		}
	}
	if a := cfg.Conductor.Account; a != "" {
		if _, ok := ids[a]; !ok {
			bad(fmt.Sprintf("conductor account %q does not exist", a), "conductor", "account")
		}
	}
	for role, target := range cfg.Routing {
		if _, ok := ids[target]; !ok {
			bad(fmt.Sprintf("routing target %q does not exist", target), "routing", role)
		}
	}
	for name, k := range cfg.Kinds {
		if k.Command == "" {
			bad("command is required", "kinds", name, "command")
		}
		if !oneOf(k.Parser, "none", "stream-json", "jsonl") {
			bad("parser must be none, stream-json or jsonl", "kinds", name, "parser")
		}
	}
}
