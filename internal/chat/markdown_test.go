package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func plainMD(text string, width int) string { return ansi.Strip(renderMarkdown(text, width)) }

func TestMarkdownInline(t *testing.T) {
	cases := map[string]string{
		"**bold** and *it* and `x := 1`": "bold and it and x := 1",
		"__b__ _i_ ~~gone~~":             "b i gone",
		"snake_case_name stays":          "snake_case_name stays",
		"a * b * c and 2*3":              "a * b * c and 2*3",
		"unclosed **bold":                "unclosed **bold",
		"see [the docs](https://x.y/z)":  "see the docs (https://x.y/z)",
		"`` a `b` ``":                    "a `b`",
		`\*not italic\*`:                 "*not italic*",
		"**bold with `code` inside**":    "bold with code inside",
		"***both***":                     "both",
	}
	for in, want := range cases {
		if got := plainMD(in, 80); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownBlocks(t *testing.T) {
	in := strings.Join([]string{
		"## Plan",
		"- first **item**",
		"  - nested",
		"1. one",
		"> quoted",
		"---",
		"```go",
		"func main() {}",
		"```",
		"after",
	}, "\n")
	want := strings.Join([]string{
		"Plan",
		"• first item",
		"  • nested",
		"1. one",
		"│ quoted",
		strings.Repeat("─", 40),
		"  func main() {}",
		"after",
	}, "\n")
	if got := plainMD(in, 60); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestMarkdownWrapsWithHangingIndent(t *testing.T) {
	got := plainMD("- alpha beta gamma delta epsilon", 16)
	want := "• alpha beta\n  gamma delta\n  epsilon"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, l := range strings.Split(plainMD("```\n"+strings.Repeat("x", 30)+"\n```", 12), "\n") {
		if ansi.StringWidth(l) > 12 {
			t.Fatalf("code line too wide: %q", l)
		}
	}
}
