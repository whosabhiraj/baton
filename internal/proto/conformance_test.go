package proto

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/whosabhiraj/baton/protocol"
)

var fixturesDir = filepath.Join("..", "..", "protocol", "fixtures")

func schemaNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(protocol.Schemas, "schema")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

func readJSON(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestAllSchemasCompile(t *testing.T) {
	names := schemaNames(t)
	if len(names) < 35 {
		t.Fatalf("only %d schemas embedded", len(names))
	}
	for _, n := range names {
		if _, err := Schema(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

// codeFor mirrors how the hub maps a schema to its error code.
func validateAs(name string, v any) *Error {
	switch {
	case name == "envelope.incoming":
		b, _ := json.Marshal(v)
		_, e := ParseIncoming(b)
		return e
	case strings.HasPrefix(name, "agent."), strings.HasPrefix(name, "inbox."), strings.HasPrefix(name, "hub."):
		p, ok := v.(map[string]any)
		if !ok {
			// Not an object: schema-level validation still has to reject it.
			if err := ValidateJSON(name, v); err != nil {
				return NewError(CodeInvalidPayload, err.Error())
			}
			return nil
		}
		typ := name[strings.Index(name, ".")+1:]
		var e *Error
		switch {
		case strings.HasPrefix(name, "agent."):
			_, e = ValidateAgentPayload(typ, p)
		case strings.HasPrefix(name, "inbox."):
			_, e = ValidateInboxPayload(typ, p)
		default:
			_, e = ValidateHubPayload(typ, p)
		}
		return e
	default:
		if err := ValidateJSON(name, v); err != nil {
			code := CodeInvalidPayload
			if strings.HasPrefix(name, "envelope.") {
				code = CodeInvalidEnvelope
			}
			return NewError(code, err.Error())
		}
		return nil
	}
}

func TestFixtures(t *testing.T) {
	names := schemaNames(t)
	nValid, nInvalid := 0, 0
	for _, name := range names {
		valids, _ := filepath.Glob(filepath.Join(fixturesDir, "valid", name, "*.json"))
		invalids, _ := filepath.Glob(filepath.Join(fixturesDir, "invalid", name, "*.json"))
		if len(valids) == 0 {
			t.Errorf("schema %s has no valid fixture", name)
		}
		if len(invalids) == 0 {
			t.Errorf("schema %s has no invalid fixture", name)
		}
		for _, f := range valids {
			nValid++
			v := readJSON(t, f)
			if err := ValidateJSON(name, v); err != nil {
				t.Errorf("valid/%s/%s: schema rejected it: %v", name, filepath.Base(f), err)
			}
			if e := validateAs(name, v); e != nil {
				t.Errorf("valid/%s/%s: validator rejected it: %v", name, filepath.Base(f), e)
			}
		}
		for _, f := range invalids {
			nInvalid++
			var c struct {
				Input json.RawMessage `json:"input"`
				Code  string          `json:"code"`
			}
			b, _ := os.ReadFile(f)
			if err := json.Unmarshal(b, &c); err != nil || c.Code == "" {
				t.Errorf("invalid/%s/%s: bad fixture file: %v", name, filepath.Base(f), err)
				continue
			}
			in, err := jsonschema.UnmarshalJSON(bytes.NewReader(c.Input))
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateJSON(name, in); err == nil {
				t.Errorf("invalid/%s/%s: schema accepted it", name, filepath.Base(f))
			}
			e := validateAs(name, in)
			if e == nil {
				t.Errorf("invalid/%s/%s: validator accepted it", name, filepath.Base(f))
			} else if e.Code != c.Code {
				t.Errorf("invalid/%s/%s: code %s, want %s", name, filepath.Base(f), e.Code, c.Code)
			}
		}
	}
	t.Logf("%d valid, %d invalid fixtures over %d schemas", nValid, nInvalid, len(names))
}

func TestFixtureDirsMatchSchemas(t *testing.T) {
	known := map[string]bool{}
	for _, n := range schemaNames(t) {
		known[n] = true
	}
	for _, kind := range []string{"valid", "invalid"} {
		entries, err := os.ReadDir(filepath.Join(fixturesDir, kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !known[e.Name()] {
				t.Errorf("%s/%s has no matching schema", kind, e.Name())
			}
		}
	}
}

// structPairs maps a schema (and optional sub-path of properties) to the Go struct that mirrors it.
var structPairs = []struct {
	schema string
	path   []string // descend through properties.<p>.items
	typ    reflect.Type
}{
	{"envelope.incoming", nil, reflect.TypeOf(Envelope{})},
	{"envelope.stored", nil, reflect.TypeOf(StoredEvent{})},
	{"inbox-message", nil, reflect.TypeOf(InboxMessage{})},
	{"run-record", nil, reflect.TypeOf(RunRecord{})},
	{"run-record", []string{"plan"}, reflect.TypeOf(PlanStep{})},
}

func TestStructsMatchSchemas(t *testing.T) {
	for _, p := range structPairs {
		sch, _ := readJSON(t, filepath.Join("..", "..", "protocol", "schema", p.schema+".json")).(map[string]any)
		for _, step := range p.path {
			sch = sch["properties"].(map[string]any)[step].(map[string]any)["items"].(map[string]any)
		}
		props, _ := sch["properties"].(map[string]any)
		required := map[string]bool{}
		for _, r := range sch["required"].([]any) {
			required[r.(string)] = true
		}
		seen := map[string]bool{}
		for i := 0; i < p.typ.NumField(); i++ {
			tag := p.typ.Field(i).Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if name == "-" || name == "" {
				continue
			}
			seen[name] = true
			prop, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s.%s: field %q not in schema %s", p.typ.Name(), p.typ.Field(i).Name, name, p.schema)
				continue
			}
			_, hasDefault := prop["default"]
			if hasDefault {
				continue
			}
			omit := strings.Contains(opts, "omitempty")
			if required[name] == omit {
				t.Errorf("%s.%s: schema required=%v but omitempty=%v", p.typ.Name(), name, required[name], omit)
			}
		}
		for name := range props {
			if !seen[name] {
				t.Errorf("schema %s property %q missing from struct %s", p.schema, name, p.typ.Name())
			}
		}
	}
}

func TestTypeListsMatchSchemas(t *testing.T) {
	have := map[string]bool{}
	for _, n := range schemaNames(t) {
		have[n] = true
	}
	for prefix, list := range map[string][]string{"agent.": AgentTypes, "hub.": HubOnlyTypes, "inbox.": InboxTypes} {
		count := 0
		for _, ty := range list {
			if !have[prefix+ty] {
				t.Errorf("no schema %s%s", prefix, ty)
			}
			count++
		}
		for n := range have {
			if strings.HasPrefix(n, prefix) && !contains(list, strings.TrimPrefix(n, prefix)) {
				t.Errorf("schema %s not in type list", n)
			}
		}
		_ = count
	}
}
