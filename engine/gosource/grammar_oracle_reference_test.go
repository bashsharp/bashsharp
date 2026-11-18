// Copyright (c) 2026, The sh Authors
// See LICENSE for licensing information

package gosource

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/importer"
	"go/scanner"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The grammar-oracle reference adapter for the go-unit dialect
// (bashsharp-tests docs/grammar-oracle/CONTRACT.md, "Reference adapters").
// The compared verdict is the vendored gc parser alone, without
// CheckBranches. The production verdict is the full production pipeline,
// Load with default options: gc's parser with CheckBranches, then the
// go/types check and lowering. It runs only when the driver passes -manifest.
var (
	oracleManifest = flag.String("manifest", "", "grammar-oracle request TSV (fixture_id dialect policy input_sha256 path)")
	oracleOut      = flag.String("out", "", "grammar-oracle reference JSONL output path")
)

type oracleSite struct {
	Kind  string `json:"kind"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type oracleLogical struct {
	File string `json:"file"`
	Line uint   `json:"line"`
	Col  uint   `json:"col"`
}

type oracleDiag struct {
	Offset  int            `json:"offset"`
	Line    int            `json:"line"`
	Col     int            `json:"col"`
	Char    int            `json:"char"`
	Msg     string         `json:"msg"`
	Logical *oracleLogical `json:"logical,omitempty"`
}

type oracleRecord struct {
	FixtureID         string       `json:"fixture_id"`
	InputSHA256       string       `json:"input_sha256"`
	Verdict           string       `json:"verdict"`
	Boundary          []oracleSite `json:"boundary"`
	Diagnostics       []oracleDiag `json:"diagnostics"`
	ProductionVerdict *string      `json:"production_verdict"`
	Phase             string       `json:"phase"`
	// The production diagnostics when only the production verdict rejects.
	ProductionDiagnostics []oracleDiag `json:"production_diagnostics,omitempty"`
	Reason                string       `json:"reason,omitempty"`
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
		if err := enc.Encode(oracleGoRecord(cols[0], cols[1], cols[2], cols[3], cols[4])); err != nil {
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

func oracleGoRecord(id, dialect, policy, sum, path string) (rec oracleRecord) {
	rec = oracleRecord{FixtureID: id, InputSHA256: sum, Boundary: []oracleSite{}, Diagnostics: []oracleDiag{}, Phase: "syntax"}
	defer func() {
		if r := recover(); r != nil {
			rec = oracleRecord{FixtureID: id, InputSHA256: sum, Verdict: "error", Boundary: []oracleSite{},
				Diagnostics: []oracleDiag{}, Phase: "syntax", Reason: fmt.Sprintf("panic: %v", r)}
		}
	}()
	if dialect != "go-unit" {
		rec.Verdict, rec.Reason = "error", fmt.Sprintf("unsupported dialect %q", dialect)
		return rec
	}
	if policy != "gcsyntax" && policy != "goparser" {
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

	// Production: Load, as the product runs it. One default importer is
	// shared across rows so each stdlib package is read once.
	prod := "accept"
	var prodErrs ErrorList
	if _, err := Load([]Source{{Name: path, Data: src}}, Options{Importer: oracleImporter}); err != nil {
		prod = "reject"
		if list, ok := err.(ErrorList); ok {
			prodErrs = list
		} else {
			prodErrs = ErrorList{err}
		}
	}
	rec.ProductionVerdict = &prod

	// Reference: lexing and parsing alone. Every gc parser or scanner
	// diagnostic rejects, whatever its prefix.
	var refErrs ErrorList
	if policy == "goparser" {
		refErrs = goParserSyntaxVerdict([]Source{{Name: path, Data: src}})
	} else {
		refErrs, _ = gcSyntaxVerdict(path, src, true, false)
	}
	for _, err := range refErrs {
		rec.Diagnostics = append(rec.Diagnostics, oracleGoDiag(src, path, err))
	}
	if len(refErrs) > 0 {
		rec.Verdict = "reject"
		return rec
	}
	rec.Verdict = "accept"
	rec.Boundary = []oracleSite{{Kind: "go-unit", Start: 0, End: len(src)}}
	for _, err := range prodErrs {
		rec.ProductionDiagnostics = append(rec.ProductionDiagnostics, oracleGoDiag(src, path, err))
	}
	if branchErrs, _ := gcSyntaxVerdict(path, src, false, false); len(branchErrs) > 0 {
		// A label, goto, break or continue error: context, not grammar.
		rec.Phase = "context"
	}
	return rec
}

var oracleImporter = defaultSourceImporter{Importer: importer.Default()}

func oracleGoDiag(src []byte, name string, err error) oracleDiag {
	switch e := err.(type) {
	case gcError:
		d := oracleDiagAt(src, oracleOffset(src, int(e.pos.Line()), int(e.pos.Col())), e.msg)
		if e.pos.Line() > 0 && (e.pos.RelFilename() != name || e.pos.RelLine() != e.pos.Line() || e.pos.RelCol() != e.pos.Col()) {
			d.Logical = &oracleLogical{File: e.pos.RelFilename(), Line: e.pos.RelLine(), Col: e.pos.RelCol()}
		}
		return d
	case *scanner.Error:
		return oracleDiagAt(src, oracleOffset(src, e.Pos.Line, e.Pos.Column), e.Msg)
	case types.Error:
		pos := e.Fset.Position(e.Pos)
		return oracleDiagAt(src, oracleOffset(src, pos.Line, pos.Column), e.Msg)
	}
	return oracleDiagAt(src, 0, err.Error())
}

// oracleOffset maps a physical 1-based line and byte column onto the
// original bytes; an unknown line or column anchors at the start.
func oracleOffset(src []byte, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return min(off+max(col-1, 0), len(src))
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
	cases := []struct {
		id, policy, src string
		badHash         bool
		verdict, prod   string
		phase           string
		offset          int // first diagnostic offset when rejecting
	}{
		{"accept", "gcsyntax", "package p\n\nfunc f() int { return 1 }\n", false, "accept", "accept", "syntax", 0},
		{"syntax-error", "gcsyntax", "package p\n\nfunc f( {\n}\n", false, "reject", "reject", "syntax", 19},
		{"bad-literal", "gcsyntax", "package p\n\nvar x = 0x\n", false, "reject", "reject", "syntax", 21},
		{"invalid-byte", "gcsyntax", "package p\n\nvar s = \"é\x00\"\n", false, "reject", "reject", "syntax", 22},
		{"branch-only", "gcsyntax", "package p\n\nfunc f() {\n\tbreak\n}\n", false, "accept", "reject", "context", 0},
		// gc's parser takes unary ~ in any expression; only the checker
		// refuses it. The production verdict is Load's.
		{"unary-tilde", "gcsyntax", "package p\n\nvar x = 1\nvar _ = ~x\n", false, "accept", "reject", "syntax", 0},
		{"undefined-name", "gcsyntax", "package p\n\nvar _ = y\n", false, "accept", "reject", "syntax", 0},
		{"goparser", "goparser", "package p\n\nfunc f( {\n}\n", false, "reject", "reject", "syntax", -1},
		{"bad-hash", "gcsyntax", "package p\n", true, "missing", "", "syntax", 0},
	}
	var tsv strings.Builder
	tsv.WriteString("fixture_id\tdialect\tpolicy\tinput_sha256\tpath\n")
	for i, c := range cases {
		path := filepath.Join(dir, fmt.Sprintf("%d.go", i))
		if err := os.WriteFile(path, []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(c.src))
		hash := hex.EncodeToString(sum[:])
		if c.badHash {
			hash = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&tsv, "%s\tgo-unit\t%s\t%s\t%s\n", c.id, c.policy, hash, path)
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
		prod := ""
		if rec.ProductionVerdict != nil {
			prod = *rec.ProductionVerdict
		}
		if rec.FixtureID != c.id || rec.Verdict != c.verdict || prod != c.prod || rec.Phase != c.phase {
			t.Errorf("%s: got id %q verdict %q production %q phase %q, want %q %q %q",
				c.id, rec.FixtureID, rec.Verdict, prod, rec.Phase, c.verdict, c.prod, c.phase)
		}
		if rec.Boundary == nil || rec.Diagnostics == nil {
			t.Errorf("%s: boundary and diagnostics must be lists: %s", c.id, lines[i])
		}
		switch c.verdict {
		case "accept":
			want := []oracleSite{{"go-unit", 0, len(c.src)}}
			if fmt.Sprint(rec.Boundary) != fmt.Sprint(want) || len(rec.Diagnostics) != 0 {
				t.Errorf("%s: got boundary %v diagnostics %v", c.id, rec.Boundary, rec.Diagnostics)
			}
			if c.prod == "reject" && len(rec.ProductionDiagnostics) == 0 {
				t.Errorf("%s: production rejected without diagnostics", c.id)
			}
		case "reject":
			if len(rec.Diagnostics) == 0 || c.offset >= 0 && rec.Diagnostics[0].Offset != c.offset {
				t.Errorf("%s: got diagnostics %v, want the first at offset %d", c.id, rec.Diagnostics, c.offset)
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

// A //line directive keeps the physical position and adds the logical one.
func TestGrammarOracleLogicalPosition(t *testing.T) {
	src := []byte("package p\n\n//line gen.go:100:1\nvar x = 0x\n")
	errs, _ := gcSyntaxVerdict("in.go", src, true, false)
	if len(errs) == 0 {
		t.Fatal("expected a literal error")
	}
	d := oracleGoDiag(src, "in.go", errs[0])
	if d.Line != 4 || d.Logical == nil || d.Logical.File != "gen.go" || d.Logical.Line != 100 {
		t.Errorf("got %+v logical %+v", d, d.Logical)
	}
}

// The committed request TSV (relative paths, resolved from the package
// directory as go test runs) must reproduce its recorded records.
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
