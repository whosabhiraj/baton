package proto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Every error code declared in errors.go must map to an explicit HTTP status (PROTOCOL.md A11).
func TestErrPathsEveryCodeHasStatus(t *testing.T) {
	want := map[string]int{
		CodeInvalidEnvelope: 400, CodeInvalidPayload: 400, CodeHubOnlyType: 400, CodeUnknownInboxType: 400,
		CodeUnauthorized: 401,
		CodeRunMismatch:  403, CodeAgentMismatch: 403, CodeForbidden: 403,
		CodeNotFound:   404,
		CodeRunEnded:   410,
		CodeInternal:   500,
		CodeUnknownQID: 409, CodeAccountDisabled: 409, CodeRoleNotAllowed: 409, CodeModelNotAllowed: 409,
		CodeAccountBusy: 409, CodeMaxAgents: 409, CodeNotARepo: 409, CodeCallsignTaken: 409,
		CodeAlreadyDecided: 409, CodeNotSupported: 409, CodeRunActive: 409, CodeMergeConflict: 409,
		CodeMergeBlocked: 409, CodeNoConductor: 409, CodeLineupRequired: 409,
		CodeAccountRequired: 400, CodeUnknownAccount: 400, CodeConfigInvalid: 400, CodeConfirmRequired: 400,
		CodeGit:          500,
		CodeLaunchFailed: 500, // the API answers with an explicit status; the default mapping is 500
	}
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(f, func(nd ast.Node) bool {
		vs, ok := nd.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if len(name.Name) < 5 || name.Name[:4] != "Code" || i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok {
				continue
			}
			code, _ := strconv.Unquote(lit.Value)
			n++
			st, known := want[code]
			if !known {
				t.Errorf("%s (%s) missing from the expected status table; add it here and to PROTOCOL.md", name.Name, code)
				continue
			}
			if got := NewError(code, "m").Status; got != st {
				t.Errorf("%s: status %d, want %d", code, got, st)
			}
		}
		return true
	})
	if n != len(want) {
		t.Errorf("errors.go declares %d codes, table has %d", n, len(want))
	}
	if NewError("SOMETHING_NEW", "").Status != 500 {
		t.Error("unknown codes must map to 500")
	}
	if e := NewError(CodeGit, ""); e.Error() != CodeGit {
		t.Errorf("empty message renders %q", e.Error())
	}
}
