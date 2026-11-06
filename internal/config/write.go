package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/whosabhiraj/baton/internal/store"
)

var writeMu sync.Mutex

// SetAccountField changes one field of one account (YAML key, e.g. "enabled",
// "max_concurrent", "models") and writes it back to the file that defines that account
// (the project file if it overrides the account, else the global file), keeping comments
// and formatting. The result is validated before writing; invalid edits are refused.
// Supported value types: bool, ints, floats, string, []string.
func SetAccountField(snap *Snapshot, accountID, field string, value any) error {
	if snap == nil {
		return errors.New("config: nil snapshot")
	}
	if field == "id" {
		return errors.New("config: account id cannot be changed")
	}
	if _, ok := yamlTags(reflect.TypeOf(Account{}))[field]; !ok {
		return fmt.Errorf("config: unknown account field %q", field)
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	gp, pp := effectivePaths(snap)
	for _, path := range []string{pp, gp} { // project first: it overrides
		if path == "" {
			continue
		}
		doc, exists, err := readDoc(path)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		acct := findAccount(doc, accountID)
		if acct == nil {
			continue
		}
		if err := setKey(acct, []string{field}, value); err != nil {
			return err
		}
		return commit(snap, path, doc)
	}
	return fmt.Errorf("config: account %q is not defined in any config file", accountID)
}

// SetHubField does the same for a hub-level key (e.g. "mode", "open_browser"); it writes to
// the project file when it exists and sets the key, else the global file. Nested keys use a
// dot ("worktrees.cleanup", "approvals.merge").
func SetHubField(snap *Snapshot, field string, value any) error {
	if snap == nil {
		return errors.New("config: nil snapshot")
	}
	segs := strings.Split(field, ".")
	t := reflect.TypeOf(Hub{})
	for i, s := range segs {
		ft, ok := yamlTags(t)[s]
		if !ok {
			return fmt.Errorf("config: unknown hub field %q", field)
		}
		if i < len(segs)-1 {
			if ft.Kind() != reflect.Struct {
				return fmt.Errorf("config: unknown hub field %q", field)
			}
			t = ft
		} else if ft.Kind() == reflect.Struct {
			return fmt.Errorf("config: hub field %q is a section, set its keys", field)
		}
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	gp, pp := effectivePaths(snap)
	full := append([]string{"hub"}, segs...)
	target := gp
	if pp != "" {
		doc, exists, err := readDoc(pp)
		if err != nil {
			return err
		}
		if exists && hasKey(doc, full) {
			target = pp
		}
	}
	doc, _, err := readDoc(target)
	if err != nil {
		return err
	}
	if err := setKey(rootMap(doc), full, value); err != nil {
		return err
	}
	return commit(snap, target, doc)
}

func effectivePaths(snap *Snapshot) (global, project string) {
	global = snap.GlobalPath
	if global == "" {
		global = GlobalPath()
	}
	return global, snap.ProjectPath
}

// readDoc parses path into a document node; a missing or empty file yields an empty
// document with a mapping root (exists=false when the file does not exist).
func readDoc(path string) (*yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	doc := &yaml.Node{}
	if exists {
		if err := yaml.Unmarshal(data, doc); err != nil {
			return nil, true, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || isNull(doc.Content[0]) {
		head := ""
		if doc.Kind == yaml.DocumentNode {
			head = doc.HeadComment
		}
		doc = &yaml.Node{Kind: yaml.DocumentNode, HeadComment: head,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, exists, fmt.Errorf("config: %s: top level is not a mapping", path)
	}
	return doc, exists, nil
}

func rootMap(doc *yaml.Node) *yaml.Node { return doc.Content[0] }

func findAccount(doc *yaml.Node, id string) *yaml.Node {
	accts := lookupExact(rootMap(doc), "accounts")
	if accts == nil || accts.Kind != yaml.SequenceNode {
		return nil
	}
	for _, a := range accts.Content {
		if got, ok := accountID(a); ok && got == id {
			return a
		}
	}
	return nil
}

func hasKey(doc *yaml.Node, path []string) bool {
	cur := rootMap(doc)
	for _, k := range path {
		cur = lookupExact(cur, k)
		if cur == nil {
			return false
		}
	}
	return !isNull(cur)
}

// setKey sets the scalar/sequence at path below m, creating mappings on the way and keeping
// the comments of a replaced value.
func setKey(m *yaml.Node, path []string, value any) error {
	for _, k := range path[:len(path)-1] {
		next := lookupExact(m, k)
		switch {
		case next == nil:
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, scalarKey(k), next)
		case isNull(next):
			*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: next.LineComment}
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("config: %s is not a mapping", k)
		}
		m = next
	}
	last := path[len(path)-1]
	var vn yaml.Node
	if err := vn.Encode(value); err != nil {
		return fmt.Errorf("config: cannot encode value for %s: %w", last, err)
	}
	if vn.Kind != yaml.ScalarNode && vn.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: unsupported value type %T for %s", value, last)
	}
	if i := keyIndex(m, last); i >= 0 {
		old := m.Content[i+1]
		vn.HeadComment, vn.LineComment, vn.FootComment = old.HeadComment, old.LineComment, old.FootComment
		if vn.Kind == yaml.SequenceNode && old.Kind == yaml.SequenceNode {
			vn.Style |= old.Style & yaml.FlowStyle
		}
		*old = vn
		return nil
	}
	m.Content = append(m.Content, scalarKey(last), &vn)
	return nil
}

func scalarKey(k string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
}

func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// commit validates the merged result with the edited file substituted, then writes it
// atomically. Only error issues that are not already present in snap are held against the edit.
func commit(snap *Snapshot, path string, doc *yaml.Node) error {
	data, err := marshalDoc(doc)
	if err != nil {
		return fmt.Errorf("config: encode %s: %w", path, err)
	}
	gp, pp := effectivePaths(snap)
	g, p := readSource(gp), readSource(pp)
	edited := source{path: path, data: data, present: true}
	if path == gp {
		g = edited
	} else {
		p = edited
	}
	res := build(g, p)
	known := map[Issue]bool{}
	for _, i := range snap.Issues {
		i.Line = 0
		known[i] = true
	}
	var fresh []string
	for _, i := range res.Issues {
		if i.Warning() {
			continue
		}
		k := i
		k.Line = 0
		if known[k] {
			continue
		}
		fresh = append(fresh, fmt.Sprintf("%s:%d %s: %s", filepath.Base(i.File), i.Line, i.Path, i.Msg))
	}
	if len(fresh) > 0 {
		return fmt.Errorf("config: edit refused, result would be invalid:\n  %s", strings.Join(fresh, "\n  "))
	}
	return writeAtomic(path, data, path == gp)
}

func writeAtomic(path string, data []byte, isGlobal bool) error {
	dir := filepath.Dir(path)
	dirPerm, mode := os.FileMode(0o755), os.FileMode(0o644)
	if isGlobal {
		dirPerm, mode = 0o700, 0o600
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return store.WriteFileAtomic(path, data, mode)
}
