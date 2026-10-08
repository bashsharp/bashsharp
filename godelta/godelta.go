// Package godelta is the single source of truth for every Go construct whose
// behaviour in Bash# differs from Go. The rows live in go-delta.tsv, which is
// embedded here, rendered as the one-page docs/go-delta.md, and served by
// `bashy explain go` as text, JSON or TSV.
package godelta

//go:generate go run gen.go

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Version is the schema version of the JSON rendering.
const Version = 1

const (
	StatusSupported = "supported"
	StatusLimited   = "limited"
	StatusGap       = "gap"
	StatusExcluded  = "excluded"
)

var validStatus = map[string]bool{StatusSupported: true, StatusLimited: true, StatusGap: true, StatusExcluded: true}

var validSurface = map[string]bool{"mixed": true, "unit": true, "island": true}

//go:embed go-delta.tsv
var tsvSource []byte

// Row is one construct that differs from Go (or one that is proven to match).
type Row struct {
	ID         string `json:"id"`
	Surface    string `json:"surface"`
	Spec       string `json:"spec"`
	Production string `json:"production"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Workaround string `json:"workaround"`
	Diagnostic string `json:"diagnostic"`
	Fixture    string `json:"fixture"`
	Exit       int    `json:"exit"`
}

var (
	once    sync.Once
	rows    []Row
	rowsErr error
)

// Rows returns the parsed table in source order.
func Rows() ([]Row, error) {
	once.Do(func() { rows, rowsErr = parse(tsvSource) })
	return rows, rowsErr
}

func parse(src []byte) ([]Row, error) {
	var out []Row
	for i, line := range strings.Split(strings.TrimRight(string(src), "\n"), "\n") {
		if i == 0 {
			continue
		}
		rec := strings.Split(line, "\t")
		if len(rec) != 10 {
			return nil, fmt.Errorf("go-delta.tsv line %d: %d fields, want 10", i+1, len(rec))
		}
		exit, err := strconv.Atoi(rec[9])
		if err != nil {
			return nil, fmt.Errorf("go-delta.tsv row %s: exit %q: %w", rec[0], rec[9], err)
		}
		out = append(out, Row{rec[0], rec[1], rec[2], rec[3], rec[4], rec[5], rec[6], rec[7], rec[8], exit})
	}
	return out, nil
}

// Lookup resolves query to rows: a row ID (case-insensitive), or any text
// that contains a row's published diagnostic, such as a whole refusal line.
func Lookup(query string) []Row {
	all, err := Rows()
	if err != nil {
		return nil
	}
	query = strings.TrimSpace(query)
	var out []Row
	for _, r := range all {
		if strings.EqualFold(r.ID, query) {
			return []Row{r}
		}
		if r.Diagnostic != "" && strings.Contains(query, r.Diagnostic) {
			out = append(out, r)
		}
	}
	return out
}

func counts(all []Row) map[string]int {
	c := map[string]int{}
	for _, r := range all {
		c[r.Status]++
	}
	return c
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	if s == "" {
		return ""
	}
	return s
}

func code(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", `\|`)
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// Markdown renders the one-page human table.
func Markdown() string {
	all, err := Rows()
	if err != nil {
		return "go-delta.tsv is malformed: " + err.Error() + "\n"
	}
	var b strings.Builder
	c := counts(all)
	b.WriteString("# Go delta: where Bash# differs from Go\n\n")
	b.WriteString("<!-- GENERATED from godelta/go-delta.tsv by `go generate ./godelta`. Do not edit. -->\n\n")
	b.WriteString("One row per Go construct whose behaviour differs from Go, keyed by Go spec section and grammar production. ")
	b.WriteString("The table is published instead of a percentage: it says which constructs, and what to do instead. ")
	b.WriteString("Every row is proven by a fixture in `bashsharp-tests/tests/go-delta/` run by `tools/go-delta-gate.sh`. ")
	b.WriteString("The same rows are available from the command line as `bashy explain go` (`--json`, `--tsv`, `--md`); `bashy explain go \"<refusal text>\"` resolves a refusal to its row and workaround.\n\n")
	b.WriteString("Go reaches Bash# on three surfaces, and the rows say which one they are about:\n\n")
	b.WriteString("- **mixed**: Go among shell statements in a `.bsh` script (`bashy --bashsharp FILE`). Bash# interprets it.\n")
	b.WriteString("- **unit**: a file that begins with a `package` clause. Bash# interprets the whole unit; this is the Tour of Go and Go by Example path.\n")
	b.WriteString("- **island**: a `~~~go` fence, `embed go \"./file.go\"`, a `.go` file or `--source=go`. The provisioned Go toolchain compiles it, so it is Go.\n\n")
	b.WriteString("Status: `supported` matches Go and is proven; `limited` works with a stated difference; `gap` is not shipped (a defect or a missing feature, with a workaround); `excluded` is refused by design.\n\n")
	fmt.Fprintf(&b, "%d rows: %d supported, %d limited, %d gap, %d excluded.\n", len(all), c[StatusSupported], c[StatusLimited], c[StatusGap], c[StatusExcluded])
	for _, s := range []struct{ key, title string }{{"mixed", "Mixed"}, {"unit", "Unit"}, {"island", "Island"}} {
		fmt.Fprintf(&b, "\n## %s\n\n", s.title)
		b.WriteString("| ID | Spec section | Production | Status | Reason | Workaround | Diagnostic | Fixture |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|\n")
		for _, r := range all {
			if r.Surface != s.key {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | `%s` |\n", r.ID, cell(r.Spec), cell(r.Production), r.Status, cell(r.Reason), cell(r.Workaround), code(r.Diagnostic), r.Fixture)
		}
	}
	return b.String()
}

func detail(w io.Writer, r Row) {
	fmt.Fprintf(w, "%s  %s  %s\n", r.ID, r.Status, r.Surface)
	fmt.Fprintf(w, "  construct:  %s (%s)\n", r.Production, r.Spec)
	fmt.Fprintf(w, "  reason:     %s\n", r.Reason)
	if r.Workaround != "" {
		fmt.Fprintf(w, "  workaround: %s\n", r.Workaround)
	}
	if r.Diagnostic != "" {
		fmt.Fprintf(w, "  diagnostic: %s\n", r.Diagnostic)
	}
	fmt.Fprintf(w, "  fixture:    tests/go-delta/%s\n", r.Fixture)
}

// Main implements `explain go`: no arguments lists the table, --json/--tsv/--md
// render it, and any other argument is a row ID or refusal text to resolve.
func Main(args []string, stdout, stderr io.Writer) int {
	all, err := Rows()
	if err != nil {
		fmt.Fprintln(stderr, "explain go:", err)
		return 2
	}
	var query []string
	format := ""
	for _, a := range args {
		switch a {
		case "--json", "--tsv", "--md":
			format = a
		case "-h", "--help":
			fmt.Fprintln(stdout, "usage: explain go [--json|--tsv|--md] [ROW-ID | REFUSAL TEXT...]")
			return 0
		default:
			query = append(query, a)
		}
	}
	matched := all
	if len(query) > 0 {
		matched = Lookup(strings.Join(query, " "))
		if len(matched) == 0 {
			fmt.Fprintf(stderr, "explain go: no row for %q; `explain go` lists them all\n", strings.Join(query, " "))
			return 1
		}
	}
	switch format {
	case "--json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Version int   `json:"version"`
			Rows    []Row `json:"rows"`
		}{Version, matched})
	case "--tsv":
		if len(query) == 0 {
			_, _ = stdout.Write(tsvSource)
			return 0
		}
		fmt.Fprintln(stdout, strings.SplitN(string(tsvSource), "\n", 2)[0])
		for _, r := range matched {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", r.ID, r.Surface, r.Spec, r.Production, r.Status, r.Reason, r.Workaround, r.Diagnostic, r.Fixture, r.Exit)
		}
	case "--md":
		if len(query) == 0 {
			fmt.Fprint(stdout, Markdown())
			return 0
		}
		fallthrough
	default:
		if len(query) == 0 {
			for _, r := range all {
				fmt.Fprintf(stdout, "%-4s %-9s %-7s %s\n", r.ID, r.Status, r.Surface, r.Production)
			}
			return 0
		}
		for i, r := range matched {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			detail(stdout, r)
		}
	}
	return 0
}
