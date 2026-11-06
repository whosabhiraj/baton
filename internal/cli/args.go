package cli

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// optDefs mirrors OPTION_DEFS in the reference cli/baton.ts (true = boolean flag), plus the
// Go-only `up` flags.
var optDefs = map[string]bool{
	"note": false, "summary": false, "evidence": false, "reason": false, "options": false,
	"blocking": true, "timeout": false, "status": false, "followups": false, "fatal": true,
	"file": false, "confirm": true, "port": false, "dev": true, "help": true,
	"data-dir": false, "admin-token-file": false,
}

// opts holds parsed flags. Presence matters (an empty --note= is "defined").
type opts struct {
	vals  map[string]string
	flags map[string]bool
}

func (o opts) has(k string) bool   { _, ok := o.vals[k]; return ok }
func (o opts) get(k string) string { return o.vals[k] }
func (o opts) flag(k string) bool  { return o.flags[k] }

// parseArgs mimics node's util.parseArgs in strict mode with allowPositionals: flags may
// appear anywhere, `--k=v` and `--k v` work, `--` ends flag parsing, last duplicate wins.
func parseArgs(args []string) (o opts, pos []string, err error) {
	o = opts{vals: map[string]string{}, flags: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			return
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			isBool, ok := optDefs[name]
			if !ok {
				return o, nil, fmt.Errorf("Unknown option '--%s'", name)
			}
			if isBool {
				if hasVal {
					return o, nil, fmt.Errorf("Option '--%s' does not take an argument", name)
				}
				o.flags[name] = true
				continue
			}
			if !hasVal {
				if i+1 >= len(args) {
					return o, nil, fmt.Errorf("Option '--%s <value>' argument missing", name)
				}
				if n := args[i+1]; len(n) > 1 && n[0] == '-' {
					return o, nil, fmt.Errorf("Option '--%s' argument is ambiguous (use --%s=VALUE for values starting with a dash)", name, name)
				}
				i++
				val = args[i]
			}
			o.vals[name] = val
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				if c != 'h' {
					return o, nil, fmt.Errorf("Unknown option '-%c'", c)
				}
				o.flags["help"] = true
			}
		default:
			pos = append(pos, a)
		}
	}
	return
}

// num mirrors the reference: finite and >= 0, else d. Whitespace is trimmed like JS Number().
func num(v string, d float64) float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return d
	}
	x, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
		return d
	}
	return x
}
