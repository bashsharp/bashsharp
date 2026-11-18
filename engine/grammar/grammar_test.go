package grammar

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func load(t *testing.T) *Grammar {
	t.Helper()
	g, err := Load("delta.gbnf")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func parse(name, src string) error {
	_, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), name)
	return err
}

func TestPublishedProductions(t *testing.T) {
	g := load(t)
	cases := []struct {
		rule, src string
		want      bool
	}{
		{"fence-open", "~~~python as py\n", true},
		{"fence-open", "~~~~c++ as lib !builder\n", true},
		{"fence-open", "~~~skill as !runner\n", true},
		{"fence-open", "~~~tf !bad-\n", false},
		{"fence-open", "~~~python nope\n", false},
		{"fence-open", "~~~python as py # note\n", false},
		{"fence", "~~~python as py\nprint(1)\n~~~~\n~~~\n", true},
		{"fence", "~~~~python\n~~~\n~~~~\n", true},
		{"fence", "~~~~~~~python\n~~~~~~\n~~~~~~~\n", true},
		{"fence", "~~~~~~~~python\n~~~~~~~\n~~~~~~~~\n", true},
		{"fence", strings.Repeat("~", 64) + "python\nbody\n" + strings.Repeat("~", 64) + "\n", true},
		{"fence", "~~~python\nx\n~~~~\n", false},
		{"fence", "~~~python\nx\n~~~ \n", false},
		{"fence", "~~~~~~~python\nx\n~~~~~~\n", false},
		{"fence", "~~~~~~~~python\nx\n~~~~~~~~~\n", false},
		{"embed-line", "embed python \"./calc.py\" as py\n", true},
		{"embed-line", "embed python \"./calc.py\" as py # note\n", true},
		{"embed-line", "embed python \"/tmp/calc.py\" as py\n", false},
		{"decorator", "@go.error()", true},
		{"decorator", "@guard(effects: \"read\")", true},
		{"decorator", "@ensure('test \"$RESULT\" = \"valid: true\napplicable: true\"')", true},
		{"decorator", "@guard(read", false},
		{"decorated-decl", "@guard(x) # why\nfunc f() {}", true},
		{"decorated-decl", "@guard(x); func f() {}", true},
		{"decorated-decl", "@guard(x)\n\n# c\nfunction f() { :; }", true},
		{"decorated-decl", "@guard(x)\necho done\n", false},
		{"decorated-decl", "@guard(x) func f() {}", false},
		{"decorated-decl", "@x() func f() {}", false},
		{"at-function", "@a()\n{\n echo\n}", true},
		{"agentic-block", "agentic { echo yes; }", true},
		{"agentic-shell-func", "agentic function helper() {\n:\n}", true},
		{"agentic-shell-func", "agentic function helper() # c\n{\n:\n}", true},
		{"agentic-typed-func", "agentic func helper(x int) int { return x }", true},
		{"agentic-typed-method", "agentic func (r Report) Name() string { return \"\" }", true},
		{"agentic-decl", "agentic function f { :; }\n", false},
		{"agentic-command", "agentic echo hi\n", true},
		{"typed-func", "func helper(x int) int { return x }", true},
		{"typed-func", "func greet(name string, retries int = 3) {\n}", true},
		{"typed-func", "func f[T any](x T) T { return x }", true},
		{"typed-func", "func f() (int, error) { return 1, nil }", true},
		{"typed-func", "func f(x int)\n{\nreturn\n}", true},
		{"typed-func", "func f() ??? (x) {}", false},
		{"typed-func", "func f( {", false},
		{"typed-method", "func (r Report) Name() string { return \"\" }", true},
		{"typed-method", "func (r Report) F( { return 1 }", false},
		{"func-decl", "func () { :; }", true},
		{"func-decl", "func f\n", true},
		{"func-decl", "func f()\n", false},
		{"block", "{ x := T{\n a: 1,\n}\n}", true},
		{"block", "{\n\tcat <<EOF\n}\nEOF\n}", true},
		{"root", "echo \"\n~~~python\n\"\n", true},
		{"root", "x='\n~~~python\n'\n", true},
		{"root", "cat <<-EOF\n\t~~~python\n\tEOF\n", true},
		{"root", "echo $(\n~~~python\n)\n", false},
		{"root", "}\n", false},
		{"root", "echo {\n", true},
		{"root", "func f() {\n\t// TODO {\n}\n", true},
		{"root", "if true; then\n  @guard(x)\n  func f() {}\nfi\n", false},
		{"root", "if true; then\n  @a()\n  {\n  :\n  }\nfi\n", true},
	}
	for _, c := range cases {
		if got := g.Match(c.rule, c.src); got != c.want {
			t.Errorf("%s %q: got %t, want %t", c.rule, c.src, got, c.want)
		}
	}
}

// Every corpus fixture gets the same verdict from the grammar and the engine,
// and the delta sites the grammar records are exactly the extension nodes the
// engine's AST places on those lines.
func TestAgreementWithPublishedCorpora(t *testing.T) {
	roots := []string{"../../bashsharp-tour", "../../bashsharp-tests/tools"}
	for _, root := range roots {
		if fi, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				t.Skipf("sibling corpus directory %s is absent", root)
			}
			t.Skipf("sibling corpus directory %s: %v", root, err)
		} else if !fi.IsDir() {
			t.Skipf("sibling corpus directory %s is not a directory", root)
		}
	}
	g := load(t)
	count, embedded, accepted, rejected := 0, 0, 0, 0
	embeddedByScript := map[string]int{}
	covered := map[string]int{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			if root == roots[0] && ext != ".bsh" || root == roots[1] && ext != ".sh" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fixtures := map[string]string{}
			if ext == ".bsh" {
				fixtures[path] = string(data)
			} else {
				fixtures = embeddedBPP(path, string(data))
				embedded += len(fixtures)
				if len(fixtures) > 0 {
					embeddedByScript[filepath.Base(path)] += len(fixtures)
				}
			}
			for fixture, src := range fixtures {
				count++
				sites, grammarErr := g.Inspect(fixture, src)
				file, parserErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), fixture)
				if (grammarErr == nil) != (parserErr == nil) {
					t.Errorf("%s: grammar: %v; parser: %v", fixture, grammarErr, parserErr)
				}
				if parserErr == nil {
					accepted++
					for rule, n := range Counts(sites) {
						covered[rule] += n
					}
					compareSites(t, fixture, sites, file)
				} else {
					rejected++
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count < 55 || embedded < 15 {
		t.Fatalf("corpus missing: checked %d fixtures, including %d embedded", count, embedded)
	}
	if embeddedByScript["polyglot-gate.sh"] != 18 || len(embeddedByScript) != 1 {
		t.Fatalf("unexpected embedded fixture inventory: %v", embeddedByScript)
	}
	for _, rule := range []string{"fence", "embed-line", "decorator", "agentic-block", "agentic-shell-func", "agentic-typed-func", "agentic-typed-method", "typed-func"} {
		if covered[rule] == 0 {
			t.Errorf("published %s production was never exercised by the corpus", rule)
		}
	}
	for _, rule := range []string{"typed-method", "at-function"} {
		if covered[rule] != 0 {
			t.Errorf("corpus now exercises %s; move it out of the testdata-only inventory", rule)
		}
	}
	if rejected != 0 {
		t.Errorf("the published corpus is expected to be all engine-accepted; %d fixtures rejected", rejected)
	}
	t.Logf("checked %d fixtures (%d Tour files, %d embedded .bpp heredocs): %d accepted, %d rejected; recognized %v", count, count-embedded, embedded, accepted, rejected, covered)
}

var bppHeredoc = regexp.MustCompile(`(?m)^cat\s+>[^\n]*\.bpp["']?\s+<<'([A-Za-z_][A-Za-z_0-9]*)'\s*$`)

func embeddedBPP(path, script string) map[string]string {
	out := map[string]string{}
	for _, m := range bppHeredoc.FindAllStringSubmatchIndex(script, -1) {
		marker := script[m[2]:m[3]]
		body := script[m[1]:]
		end := strings.Index(body, "\n"+marker+"\n")
		if end < 0 {
			continue
		}
		line := strings.Count(script[:m[0]], "\n") + 1
		out[filepath.Base(path)+":"+strconv.Itoa(line)] = body[:end] + "\n"
	}
	return out
}

// Negative and positive agreement on the local fixture set. The directory
// name states the expected verdict; both recognizers must produce it.
func TestFixtureAgreement(t *testing.T) {
	g := load(t)
	seen := map[string]int{}
	for _, want := range []string{"accept", "reject"} {
		paths, err := filepath.Glob(filepath.Join("testdata", want, "*.bsh"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no testdata/%s fixtures: %v", want, err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			sites, grammarErr := g.Inspect(path, src)
			file, parserErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), path)
			if (parserErr == nil) != (want == "accept") {
				t.Errorf("%s: engine verdict %v contradicts the fixture's expected %s", path, parserErr, want)
			}
			if (grammarErr == nil) != (want == "accept") {
				t.Errorf("%s: grammar verdict %v contradicts the fixture's expected %s", path, grammarErr, want)
			}
			if parserErr == nil && grammarErr == nil {
				compareSites(t, path, sites, file)
			}
			for rule, n := range Counts(sites) {
				seen[want+":"+rule] += n
			}
		}
	}
	for _, key := range []string{"accept:embed-line", "accept:typed-method", "accept:at-function", "accept:decorator", "accept:fence"} {
		if seen[key] == 0 {
			t.Errorf("testdata never exercises %s", key)
		}
	}
	t.Logf("testdata sites: %v", seen)
}

// Inline agreement table: committed and near-miss spellings for every delta
// production, each compared with the engine. The expectation column is a
// third voice so a silent change in either recognizer is visible.
func TestSpellingAgreement(t *testing.T) {
	g := load(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "calc.py"), []byte("def add(a, b): return a + b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "x.bsh")
	cases := []struct {
		src    string
		accept bool
	}{
		{"~~~python as py # note\nx\n~~~\n", true}, // near miss: ordinary command lines
		{"~~~python as py \nx\n~~~\n", true},
		{"~~~python\nx\n~~~ \n", false},
		{"~~~python\nx\n~~~~\n", false},
		{" ~~~python\nx\n~~~\n", true},
		{"~~~python as\nx\n~~~\n", true},
		{"~~~python as py!run\nx\n~~~\n", true},
		{"~~~python as py extra\nx\n~~~\n", true},
		{"~~~python\n", false},
		{"~~~python\nx", false},
		{"~~~python\nx\n~~~", true},
		{"~~~\nx\n~~~\n", true},
		{"~~~Python\nx\n~~~\n", true},
		{"~~~c++ as lib !builder\nx\n~~~\n", true},
		{"~~~python as !run\nx\n~~~\n", true},
		{"~~~python !run\nx\n~~~\n", true},
		{"~~~python as py\n~~~~\n~~~\n", true},
		// The parser and the published grammar must agree for arbitrary fence
		// widths, not only the historical 3–6 enumeration.
		{"~~~~~~~python\nbody\n~~~~~~~\n", true},
		{"~~~~~~~~python\nbody\n~~~~~~~~\n", true},
		{strings.Repeat("~", 64) + "python\nbody\n" + strings.Repeat("~", 64) + "\n", true},
		{"~~~~~~~python\nbody\n~~~~~~\n", false},
		{"~~~~~~~~python\nbody\n~~~~~~~~~\n", false},
		{"function f() {\n  ~~~python\n  x\n  ~~~\n}\n", true},
		{"cat <<EOF\n~~~python\nEOF\n", true},
		{"cat <<-EOF\n\t~~~python\n\tEOF\n", true},
		{"x='\n~~~python\n'\n", true},
		{"echo \"\n~~~python\n\"\n", true},
		{"echo \\\n~~~python\n", true},
		{"echo `\n~~~python\n`\n", false},
		{"echo $(\n~~~python\n)\n", false},
		{"embed python \"./calc.py\" as py\n", true},
		{"embed python \"./calc.py\" as py # note\n", true},
		{"embed python \"./calc.py\"\n", true},
		{"embed python \"./calc.py\" as !run\n", true},
		{"embed python \"./calc.py\" !run\n", true},
		{"embed python \"./missing.py\" as py\n", false},
		{"embed python ./calc.py as py\n", true},
		{"embed python \"./calc.py\" as py extra\n", true},
		{"embed python\n", true},
		{"@guard(x) # why\nfunc f() {}\n", true},
		{"@guard(x); func f() {}\n", true},
		{"@guard(x)\n\nfunc f() {}\n", true},
		{"@guard(x)\n# c\nfunc f() {}\n", true},
		{"@guard(x)\n@log()\nfunc f() {}\n", true},
		{"@guard(x)\n@go.error()\nfunc f() error {}\n", true},
		{"@go.error()\nfunc f() {}\n", true},
		{"@go.error()\nfunction f() { :; }\n", true}, // syntactically a decorator; the evaluator enforces go.error semantics
		{"@go.error(1)\nfunc f() {}\n", true},
		{"@go.errors()\nfunc f() {}\n", true},
		{"@go.error_x(1)\nfunc f() {}\n", true},
		{"@guard(\"read\")\necho done\n", false},
		{"@guard(read\nfunc f() {}\n", false},
		{"@guard(\n\"read\"\n)\nfunc f() {}\n", false},
		{"  @guard(x)\n  func f() {}\n", false},
		{"@x\n", true},
		{"@x()\n", false},
		{"@x(1)\n", false},
		{"@x() func f() {}\n", false},
		{"@a()\n{\n echo\n}\n", true},
		{"agentic { echo yes; }\n", true},
		{"agentic {\necho open\n", false},
		{"agentic {\n", false},
		{"agentic f() { :; }\n", false},
		{"agentic function f { :; }\n", false},
		{"agentic function f()\n{\n:\n}\n", true},
		{"agentic function f() # c\n{\n:\n}\n", true},
		{"agentic function f()  {\n:\n}\n", true},
		{"agentic function f( { echo bad; }\n", false},
		{"agentic func f(x int) int {\nreturn x\n}\n", true},
		{"agentic func f( { return 1 }\n", false},
		{"agentic\n", true},
		{"agentic echo hi\n", true},
		{"agentic function\n", true},
		{"if true; then\n  agentic { echo; }\nfi\n", true},
		{"agentic {\n  ~~~python as py\n }\n  ~~~\n}\n", false},
		{"func f(x int) int { return x }\n", true},
		{"func f[T any](x T) T { return x }\n", true},
		{"func f() (int, error) { return 1, nil }\n", true},
		{"func f(x int)\n{\nreturn\n}\n", true},
		{"func f() { :; }\n", true},
		{"func () { :; }\n", true},
		{"func (r Report) F() int { return 1 }\n", true},
		{"func (r Report) F( { return 1 }\n", false},
		{"func f( {\n", false},
		{"func f() ??? (x) {}\n", false},
		{"func f() ??? { return 1 }\n", false},
		{"func f() int {\nreturn 1\n", false},
		{"func f()\n", false},
		{"func f\n", true},
		{"func\n", true},
		{"typeset -f func\n", true},
		{"func f() int {\n// TODO {\nreturn 1\n}\n", true},
		{"func f() int {\n# TODO {\nreturn 1\n}\n", true},
		{"func f() string {\nreturn \"}\"\n}\n", true},
		{"func f() int {\n\tr := '}'\n\treturn 1\n}\n", true},
		{"func f() int {\n\techo \"a\" # }\n\treturn 1\n}\n", true},
		{"func f() int {\n\tcat <<EOF\n}\nEOF\n\treturn 1\n}\n", true},
		{"func f() int {\n\t/* } */\n\treturn 1\n}\n", false},
		{"func f() {\n}\necho after\n", true},
		{"if true; then\n  func f() {}\nfi\n", true},
	}
	for _, c := range cases {
		parserErr := parse(name, c.src)
		grammarErr := g.CheckDeltaFile(name, c.src)
		if (parserErr == nil) != c.accept {
			t.Errorf("engine verdict for %q changed: %v (expected accept=%t)", c.src, parserErr, c.accept)
		}
		if (grammarErr == nil) != c.accept {
			t.Errorf("grammar verdict for %q: %v (expected accept=%t)", c.src, grammarErr, c.accept)
		}
	}
}

// Known divergences: spellings where the opaque base-Bash fragment is coarser
// than the engine. Each is asserted so a fix or a regression is visible, and
// README.md lists the same set.
func TestKnownDivergences(t *testing.T) {
	g := load(t)
	cases := []struct {
		src             string
		engine, grammar bool
	}{
		// A standalone `}` word in argument position closes a block to the fragment scanner.
		{"echo }\n", true, false},
		{"echo { x }\n", true, false},
		// A brace group after `;` on the same line: the line head is `echo`, so `{` is a word.
		{"echo a; { echo; }\n", true, false},
		// Backticks are not tracked across lines, so a `}` inside one closes nothing.
		{"s=`echo\n}`\n", false, true},
	}
	for _, c := range cases {
		if got := parse("x.bsh", c.src) == nil; got != c.engine {
			t.Errorf("engine verdict for %q changed: accept=%t", c.src, got)
		}
		if got := g.CheckDelta(c.src) == nil; got != c.grammar {
			t.Errorf("grammar verdict for %q changed: accept=%t", c.src, got)
		}
	}
}

// The EBNF is the specification twin of the executable GBNF: the same set of
// productions, each referencing the same nonterminals, and every referenced
// nonterminal defined or declared external. Names map `_` to `-`.
func TestEBNFMirrorsGBNF(t *testing.T) {
	g := load(t)
	data, err := os.ReadFile("delta.ebnf")
	if err != nil {
		t.Fatal(err)
	}
	ebnf, externals, err := parseEBNF(string(data))
	if err != nil {
		t.Fatal(err)
	}
	gbnfRules := map[string]bool{}
	for _, r := range g.Rules() {
		gbnfRules[r] = true
	}
	for name, refs := range ebnf {
		if !gbnfRules[name] && !externals[name] {
			t.Errorf("EBNF production %s has no GBNF rule", name)
			continue
		}
		for _, r := range refs {
			if _, ok := ebnf[r]; !ok && !externals[r] {
				t.Errorf("EBNF %s references undefined %s", name, r)
			}
		}
		if externals[name] {
			continue
		}
		want := uniqueSorted(g.References(name))
		got := uniqueSorted(refs)
		if strings.Join(want, " ") != strings.Join(got, " ") {
			t.Errorf("EBNF %s references %v; GBNF references %v", name, got, want)
		}
	}
	for name := range gbnfRules {
		if _, ok := ebnf[name]; !ok {
			t.Errorf("GBNF rule %s has no EBNF production", name)
		}
	}
	for _, opaque := range OpaqueRules {
		if !externals[opaque] {
			t.Errorf("opaque %s must be declared external in the EBNF", opaque)
		}
	}
	for name := range externals {
		if _, ok := ebnf[name]; !ok {
			t.Errorf("external %s declared but never produced", name)
		}
	}
}

var ebnfProduction = regexp.MustCompile(`(?s)([a-z][a-z0-9_]*)\s*=\s*(.*?)\s*;`)

// parseEBNF splits ISO-style productions. A production whose body is a single
// special sequence `? ... ?` is an external. Terminals and comments are
// removed before nonterminal references are collected.
func parseEBNF(src string) (map[string][]string, map[string]bool, error) {
	src = regexp.MustCompile(`(?s)\(\*.*?\*\)`).ReplaceAllString(src, "")
	rules := map[string][]string{}
	externals := map[string]bool{}
	for _, m := range ebnfProduction.FindAllStringSubmatch(src, -1) {
		name, body := strings.ReplaceAll(m[1], "_", "-"), m[2]
		if _, dup := rules[name]; dup {
			return nil, nil, os.ErrExist
		}
		if strings.HasPrefix(body, "?") && strings.HasSuffix(body, "?") && strings.Count(body, "?") == 2 {
			externals[name] = true
			rules[name] = nil
			continue
		}
		body = regexp.MustCompile(`"[^"]*"|'[^']*'|\?[^?]*\?`).ReplaceAllString(body, " ")
		var refs []string
		for _, id := range regexp.MustCompile(`[a-z][a-z0-9_]*`).FindAllString(body, -1) {
			refs = append(refs, strings.ReplaceAll(id, "_", "-"))
		}
		rules[name] = refs
	}
	return rules, externals, nil
}

func uniqueSorted(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// compareSites requires the grammar's recorded sites and the engine's AST
// extension nodes to agree line by line.
func compareSites(t *testing.T, path string, sites []Site, file *syntax.File) {
	t.Helper()
	got := map[string]bool{}
	for _, s := range sites {
		if s.Rule == "shell-function" {
			continue // Bash owns these; recorded only as decorator targets
		}
		got[s.Rule+"@"+strconv.Itoa(s.Line)] = true
	}
	want := map[string]bool{}
	add := func(pos syntax.Pos, rule string) { want[rule+"@"+strconv.Itoa(int(pos.Line()))] = true }
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.SourceBlock:
			if n.Src != nil {
				add(n.Pos(), "embed-line")
			} else {
				add(n.Pos(), "fence")
			}
		case *syntax.BashPPDecorator:
			add(n.Pos(), "decorator")
		case *syntax.BashPPAgenticBlock:
			add(n.Pos(), "agentic-block")
		case *syntax.BashPPFuncDecl:
			switch {
			case n.Agentic != nil && n.Receiver != nil:
				add(n.Agentic.Pos(), "agentic-typed-method")
			case n.Agentic != nil:
				add(n.Agentic.Pos(), "agentic-typed-func")
			case n.Receiver != nil:
				add(n.Kw.Pos(), "typed-method")
			default:
				add(n.Kw.Pos(), "typed-func")
			}
		case *syntax.FuncDecl:
			if n.Agentic != nil {
				add(n.Agentic.Pos(), "agentic-shell-func")
			} else if strings.HasPrefix(n.Name.Value, "@") {
				add(n.Pos(), "at-function")
			}
		}
		return true
	})
	for k := range want {
		if !got[k] {
			t.Errorf("%s: engine site %s has no grammar site", path, k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("%s: grammar site %s has no engine site", path, k)
		}
	}
}
