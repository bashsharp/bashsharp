// Copyright (c) 2026, The sh Authors
// See LICENSE for licensing information

package syntax

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The grammar-oracle reference adapter (bashsharp-tests
// docs/grammar-oracle/CONTRACT.md, "Reference adapters"). It runs only when
// the driver passes -manifest; otherwise TestGrammarOracleReference skips.
var (
	oracleManifest = flag.String("manifest", "", "grammar-oracle request TSV (fixture_id dialect policy input_sha256 path)")
	oracleOut      = flag.String("out", "", "grammar-oracle reference JSONL output path")
)

type oracleSite struct {
	Kind  string `json:"kind"`
	Start uint   `json:"start"`
	End   uint   `json:"end"`
}

type oracleDiag struct {
	Offset int    `json:"offset"`
	Line   int    `json:"line"`
	Col    int    `json:"col"`
	Char   int    `json:"char"`
	Msg    string `json:"msg"`
}

type oracleRecord struct {
	FixtureID         string       `json:"fixture_id"`
	InputSHA256       string       `json:"input_sha256"`
	Verdict           string       `json:"verdict"`
	Boundary          []oracleSite `json:"boundary"`
	Diagnostics       []oracleDiag `json:"diagnostics"`
	ProductionVerdict *string      `json:"production_verdict"` // gosource only
	Phase             string       `json:"phase"`
	Reason            string       `json:"reason,omitempty"`
}

func TestGrammarOracleReference(t *testing.T) {
	if *oracleManifest == "" {
		t.Skip("grammar-oracle reference adapter: no -manifest given")
	}
	if *oracleOut == "" {
		t.Fatal("-manifest requires -out")
	}
	in, err := os.Open(*oracleManifest)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(*oracleOut)
	if err != nil {
		t.Fatal(err)
	}
	n, err := oracleReference(in, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d reference records to %s", n, *oracleOut)
}

// oracleReference reads request rows and writes one record per row, then the
// done line. A malformed manifest is an error: the driver then sees no done
// line and marks every fixture missing.
func oracleReference(manifest io.Reader, w io.Writer) (int, error) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	scan := bufio.NewScanner(manifest)
	scan.Buffer(make([]byte, 1<<20), 1<<20)
	count := 0
	for lineNum := 1; scan.Scan(); lineNum++ {
		line := strings.TrimSuffix(scan.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "fixture_id\t") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 5 {
			return count, fmt.Errorf("manifest line %d: want 5 tab-separated columns, got %d", lineNum, len(cols))
		}
		if err := enc.Encode(oracleSyntaxRecord(cols[0], cols[1], cols[2], cols[3], cols[4])); err != nil {
			return count, err
		}
		count++
	}
	if err := scan.Err(); err != nil {
		return count, err
	}
	return count, enc.Encode(struct {
		Done  bool `json:"done"`
		Count int  `json:"count"`
	}{true, count})
}

func oracleSyntaxRecord(id, dialect, policy, sum, path string) (rec oracleRecord) {
	rec = oracleRecord{FixtureID: id, InputSHA256: sum, Boundary: []oracleSite{}, Diagnostics: []oracleDiag{}, Phase: "syntax"}
	defer func() {
		if r := recover(); r != nil {
			rec.Verdict, rec.Boundary, rec.Diagnostics = "error", []oracleSite{}, []oracleDiag{}
			rec.Reason = fmt.Sprintf("panic: %v", r)
		}
	}()
	var lang LangVariant
	switch dialect {
	case "bash":
		lang = LangBash
	case "bashsharp":
		lang = LangBashPP
	default:
		rec.Verdict, rec.Reason = "error", fmt.Sprintf("unsupported dialect %q", dialect)
		return rec
	}
	if policy != "sh-syntax" {
		rec.Verdict, rec.Reason = "error", fmt.Sprintf("unsupported policy %q", policy)
		return rec
	}
	src, err := os.ReadFile(path)
	if err != nil {
		rec.Verdict, rec.Reason = "error", err.Error()
		return rec
	}
	if got := sha256.Sum256(src); hex.EncodeToString(got[:]) != sum {
		rec.Verdict = "missing"
		rec.Reason = fmt.Sprintf("input hash mismatch: manifest %s, file %s", sum, hex.EncodeToString(got[:]))
		return rec
	}
	// The path is the parse name so relative embed targets resolve in the
	// input's own directory; Bash# has no alias expansion at parse time.
	file, err := NewParser(Variant(lang)).Parse(bytes.NewReader(src), path)
	if err != nil {
		rec.Verdict = "reject"
		rec.Diagnostics = append(rec.Diagnostics, oracleSyntaxDiag(src, path, err))
		if pe, ok := err.(ParseError); ok {
			switch {
			case strings.HasPrefix(pe.Text, "embed: "):
				rec.Phase = "context" // embed-target availability is not grammar
			case strings.HasPrefix(pe.Text, "invalid import statement") && oracleImportPathRefusal(src, int(pe.Pos.Offset())):
				rec.Phase = "context" // module-path validity is not grammar
			}
		}
		return rec
	}
	rec.Verdict = "accept"
	if lang == LangBashPP {
		// With Bash# off there are no extension sites: an @name shell
		// function is a plain bash function there.
		rec.Boundary = oracleSites(file)
	}
	return rec
}

// oracleImportPathRefusal reports whether the import statement at off is
// refused only for its path: the line is the exact single-spec shape
// bashppImport recognizes, `import [ALIAS] "PATH"`, and PATH fails
// module.CheckImportPath (validBashPPImportPath). An empty literal "" is the
// Go string_lit for the empty path; its check fails too.
func oracleImportPathRefusal(src []byte, off int) bool {
	line := src[off:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	file, err := NewParser(Variant(LangBash)).Parse(bytes.NewReader(line), "")
	if err != nil || len(file.Stmts) != 1 || len(file.Stmts[0].Redirs) != 0 {
		return false
	}
	ce, ok := file.Stmts[0].Cmd.(*CallExpr)
	if !ok || len(ce.Assigns) != 0 || (len(ce.Args) != 2 && len(ce.Args) != 3) {
		return false
	}
	if kw := bashppBareLit(ce.Args[0]); kw == nil || kw.Value != "import" {
		return false
	}
	if len(ce.Args) == 3 {
		if alias := bashppBareLit(ce.Args[1]); alias == nil || !bashppImportAlias(alias.Value) {
			return false
		}
	}
	word := ce.Args[len(ce.Args)-1]
	if len(word.Parts) != 1 {
		return false
	}
	q, ok := word.Parts[0].(*DblQuoted)
	if !ok || q.Dollar || len(q.Parts) > 1 {
		return false
	}
	lit := ""
	if len(q.Parts) == 1 {
		l, ok := q.Parts[0].(*Lit)
		if !ok {
			return false
		}
		lit = l.Value
	}
	path, err := strconv.Unquote(`"` + lit + `"`)
	return err == nil && !validBashPPImportPath(path)
}

// oracleSites lists the extension nodes in source order, keyed and anchored
// as sh/grammar compareSites keys them; a span runs to the node's End.
func oracleSites(file *File) []oracleSite {
	sites := []oracleSite{}
	add := func(start Pos, n Node, kind string) {
		sites = append(sites, oracleSite{Kind: kind, Start: start.Offset(), End: n.End().Offset()})
	}
	Walk(file, func(node Node) bool {
		switch n := node.(type) {
		case *SourceBlock:
			if n.Src != nil {
				add(n.Pos(), n, "embed-line")
			} else {
				add(n.Pos(), n, "fence")
			}
		case *BashPPDecorator:
			add(n.Pos(), n, "decorator")
		case *BashPPAgenticBlock:
			add(n.Pos(), n, "agentic-block")
		case *BashPPFuncDecl:
			switch {
			case n.Agentic != nil && n.Receiver != nil:
				add(n.Agentic.Pos(), n, "agentic-typed-method")
			case n.Agentic != nil:
				add(n.Agentic.Pos(), n, "agentic-typed-func")
			case n.Receiver != nil:
				add(n.Kw.Pos(), n, "typed-method")
			default:
				add(n.Kw.Pos(), n, "typed-func")
			}
		case *FuncDecl:
			if n.Agentic != nil {
				add(n.Agentic.Pos(), n, "agentic-shell-func")
			} else if strings.HasPrefix(n.Name.Value, "@") {
				add(n.Pos(), n, "at-function")
			}
		}
		return true
	})
	slices.SortStableFunc(sites, func(a, b oracleSite) int { return cmp.Compare(a.Start, b.Start) })
	return sites
}

func oracleSyntaxDiag(src []byte, name string, err error) oracleDiag {
	var pos Pos
	msg := err.Error()
	switch e := err.(type) {
	case ParseError:
		pos, msg = e.Pos, e.Text
	case LangError:
		pos = e.Pos
		msg = strings.TrimPrefix(msg, name+":"+e.Pos.String()+": ")
	}
	off := 0
	if pos.IsValid() {
		off = int(pos.Offset())
	}
	return oracleDiagAt(src, off, msg)
}

// oracleDiagAt places a diagnostic on the original bytes: only LF breaks a
// line, col is the 1-based byte column and char the 1-based code-point
// column, an invalid UTF-8 byte counting as one.
func oracleDiagAt(src []byte, off int, msg string) oracleDiag {
	off = min(max(off, 0), len(src))
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	return oracleDiag{
		Offset: off,
		Line:   bytes.Count(src[:off], []byte("\n")) + 1,
		Col:    off - start + 1,
		Char:   utf8.RuneCount(src[start:off]) + 1,
		Msg:    msg,
	}
}

// Without -manifest: a generated request TSV through the adapter function.
func TestGrammarOracleReferenceAdapter(t *testing.T) {
	dir := t.TempDir()
	type want struct {
		verdict string
		sites   []oracleSite
		offset  int    // first diagnostic offset when rejecting
		phase   string // "" is syntax
	}
	cases := []struct {
		id, dialect, src string
		badHash          bool
		want             want
	}{
		// The four sh/grammar TestKnownDivergences inputs, with what
		// sh/syntax (LangBashPP) says for each.
		{"div/echo-close", "bashsharp", "echo }\n", false, want{verdict: "accept"}},
		{"div/echo-braces", "bashsharp", "echo { x }\n", false, want{verdict: "accept"}},
		{"div/group-after-semi", "bashsharp", "echo a; { echo; }\n", false, want{verdict: "accept"}},
		{"div/backtick-close", "bashsharp", "s=`echo\n}`\n", false, want{verdict: "reject", offset: 8}},
		{"typed-func", "bashsharp", "echo hi\nfunc f(x int) int { return x }\n", false,
			want{verdict: "accept", sites: []oracleSite{{"typed-func", 8, 38}}}},
		{"typed-func-in-bash", "bash", "func f(x int) int { return x }\n", false, want{verdict: "reject", offset: 6}},
		// An @name shell function is an at-function site only with Bash# on.
		{"at-function", "bashsharp", "@d() { echo; }\n", false,
			want{verdict: "accept", sites: []oracleSite{{"at-function", 0, 14}}}},
		{"at-function-in-bash", "bash", "@d() { echo; }\n", false, want{verdict: "accept"}},
		// A module-path refusal is context; a refused shape stays syntax.
		{"import-empty-path", "bashsharp", "import \"\"\necho hi\n", false, want{verdict: "reject", phase: "context"}},
		{"import-invalid-path", "bashsharp", "import x \"a b\"\n", false, want{verdict: "reject", phase: "context"}},
		{"import-bad-shape", "bashsharp", "import \"fmt\" extra\n", false, want{verdict: "reject"}},
		{"bad-hash", "bashsharp", "echo ok\n", true, want{verdict: "missing"}},
	}
	var tsv strings.Builder
	tsv.WriteString("fixture_id\tdialect\tpolicy\tinput_sha256\tpath\n")
	for i, c := range cases {
		path := filepath.Join(dir, fmt.Sprintf("%d.bsh", i))
		if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(c.src))
		hash := hex.EncodeToString(sum[:])
		if c.badHash {
			hash = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&tsv, "%s\t%s\tsh-syntax\t%s\t%s\n", c.id, c.dialect, hash, path)
	}
	var out bytes.Buffer
	n, err := oracleReference(strings.NewReader(tsv.String()), &out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if n != len(cases) || len(lines) != len(cases)+1 {
		t.Fatalf("got %d records and %d lines for %d rows:\n%s", n, len(lines), len(cases), out.String())
	}
	for i, c := range cases {
		if !json.Valid([]byte(lines[i])) {
			t.Fatalf("%s: invalid JSON %s", c.id, lines[i])
		}
		var rec oracleRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", lines[i])
		if rec.FixtureID != c.id || rec.Verdict != c.want.verdict {
			t.Errorf("%s: got id %q verdict %q, want %q", c.id, rec.FixtureID, rec.Verdict, c.want.verdict)
		}
		if phase := cmp.Or(c.want.phase, "syntax"); rec.Phase != phase {
			t.Errorf("%s: got phase %q, want %q", c.id, rec.Phase, phase)
		}
		if rec.Boundary == nil || rec.Diagnostics == nil || rec.ProductionVerdict != nil {
			t.Errorf("%s: boundary/diagnostics must be lists and production_verdict null: %s", c.id, lines[i])
		}
		switch c.want.verdict {
		case "accept":
			if fmt.Sprint(rec.Boundary) != fmt.Sprint(append([]oracleSite{}, c.want.sites...)) || len(rec.Diagnostics) != 0 {
				t.Errorf("%s: got boundary %v diagnostics %v, want %v", c.id, rec.Boundary, rec.Diagnostics, c.want.sites)
			}
		case "reject":
			if len(rec.Diagnostics) != 1 || rec.Diagnostics[0].Offset != c.want.offset {
				t.Errorf("%s: got diagnostics %v, want one at offset %d", c.id, rec.Diagnostics, c.want.offset)
			}
		case "missing":
			if !strings.Contains(rec.Reason, "hash mismatch") {
				t.Errorf("%s: got reason %q", c.id, rec.Reason)
			}
		}
	}
	if want := fmt.Sprintf(`{"done":true,"count":%d}`, len(cases)); lines[len(cases)] != want {
		t.Errorf("done line: got %s, want %s", lines[len(cases)], want)
	}
}

// Positions are on the original bytes: LF-only lines, a CR stays in its
// line, byte column versus code-point column.
func TestGrammarOracleDiagPositions(t *testing.T) {
	src := []byte("a\r\nxé\xffz")
	d := oracleDiagAt(src, 8, "m")
	if d.Offset != 8 || d.Line != 2 || d.Col != 6 || d.Char != 5 {
		t.Errorf("got %+v", d)
	}
}

// The committed request TSV (relative paths, resolved from the package
// directory as go test runs) must reproduce its recorded verdicts.
func TestGrammarOracleReferenceTestdata(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "grammar-oracle", "request.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if _, err := oracleReference(f, &out); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "grammar-oracle", "reference.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("reference records changed:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
}
