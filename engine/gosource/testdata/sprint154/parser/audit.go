// Package audit is the Sprint 154 parser-exclusive causal audit (S154.3).
//
// It is an AUDIT tool, never a gate: it re-implements just enough of the
// upstream cmd/internal/testdir errorcheck comparison (wantedErrors +
// errorCheck, quoted where relied upon) to classify, per corpus root, which
// Bash++ front-end stage first diverges from gc's expected diagnostics and
// how. The leaf run under the upstream harness stays the only authority.
package audit

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Stage is the front-end component that produced a diagnostic.
type Stage int

const (
	StagePass    Stage = iota // no failure
	StageScanner              // go/scanner (or gc's scanner)
	StageParser               // go/parser (or gc's syntax parser)
	StageChecker              // go/types, plus Bash++'s own post-parse diagnostics
)

func (s Stage) String() string {
	switch s {
	case StageScanner:
		return "scanner"
	case StageParser:
		return "parser"
	case StageChecker:
		return "checker"
	}
	return "pass"
}

// Class is the verdict class of one failure.
type Class int

const (
	ClassPass         Class = iota
	ClassWording            // a diagnostic on the expected line that the regex does not match
	ClassPosition           // the expected message appears, but on another line
	ClassMultiplicity       // the expected line matches, but more (unmatched) diagnostics sit on it
	ClassExtra              // a diagnostic on a line with no expectation
	ClassMissing            // an expected line with no diagnostic at all
)

func (c Class) String() string {
	return [...]string{"pass", "wording", "position", "multiplicity", "extra", "missing"}[c]
}

// Diag is one front-end diagnostic in "file:line:col: msg" form. Continuation
// lines ("\t"-prefixed) are already folded into Raw, as upstream splitOutput
// does. Stage is the component that produced it.
type Diag struct {
	Raw   string
	Line  int
	Stage Stage
}

// Text is the message with the leading "file:line:col:" cut, the way upstream
// errorCheck matches: `if _, suffix, ok := strings.Cut(text, " "); ok { text = suffix }`.
func (d Diag) Text() string {
	if _, suffix, ok := strings.Cut(d.Raw, " "); ok {
		return suffix
	}
	return d.Raw
}

// Want is one `// ERROR "regex"` expectation, one per quoted regex.
type Want struct {
	Line  int
	ReStr string
	Re    *regexp.Regexp
	Auto  bool
}

// Failure is one divergence from the expectations.
type Failure struct {
	Stage Stage
	Class Class
	Line  int    // the expected line (missing/position/wording/multiplicity) or the diagnostic's line (extra)
	Want  string // the expected regex, if any
	Got   string // the offending diagnostic's raw text, if any
}

// Result is the verdict for one root.
type Result struct {
	Root     string
	Header   string
	Failures []Failure // sorted by (stage, line)
	// DupMatched counts diagnostics that matched an expectation already
	// satisfied by another diagnostic on the same line. Upstream consumes
	// all matching messages, so duplicates alone never fail a root.
	DupMatched int
	// CheckerAfterSyntax is set when the root has scanner/parser diagnostics
	// and checker diagnostics too. gc stops after syntax errors; go/types is
	// still run by the Bash++ front end.
	CheckerAfterSyntax bool
	// RelatedFolded counts go/types related-information diagnostics (Msg
	// beginning with "\t", e.g. "\tprevious case") that FrontEnd folded into
	// the diagnostic before them, the way gc prints them. ErrorList.Error()
	// prints each on its own "file:line:col: \t..." line, which the upstream
	// harness would count as an Unmatched Error.
	RelatedFolded int
	NDiags        int
	NWants        int
}

// Stage is the first failing stage: the lowest stage among the failures.
func (r Result) FirstStage() Stage {
	if len(r.Failures) == 0 {
		return StagePass
	}
	return r.Failures[0].Stage
}

// FirstClass is the class of the first failure in the first failing stage.
func (r Result) FirstClass() Class {
	if len(r.Failures) == 0 {
		return ClassPass
	}
	return r.Failures[0].Class
}

// Upstream cmd/internal/testdir/testdir_test.go (Go 1.27.0):
//
//	errRx       = regexp.MustCompile(`// (?:GC_)?ERROR (.*)`)
//	errAutoRx   = regexp.MustCompile(`// (?:GC_)?ERRORAUTO (.*)`)
//	errQuotesRx = regexp.MustCompile(`"([^"]*)"`)
//	lineRx      = regexp.MustCompile(`LINE(([+-])(\d+))?`)
var (
	errRx       = regexp.MustCompile(`// (?:GC_)?ERROR (.*)`)
	errAutoRx   = regexp.MustCompile(`// (?:GC_)?ERRORAUTO (.*)`)
	errQuotesRx = regexp.MustCompile(`"([^"]*)"`)
	lineRx      = regexp.MustCompile(`LINE(([+-])(\d+))?`)
)

// WantedErrors collects the expectations of one source file the way upstream
// wantedErrors does: a line containing "////" is skipped ("double comment
// disables ERROR"); ERRORAUTO wins over ERROR; every quoted string on the
// directive is one regex; LINE, LINE+n, LINE-n are rewritten to "short:N".
func WantedErrors(src []byte, short string) ([]Want, error) {
	var wants []Want
	for i, line := range strings.Split(string(src), "\n") {
		lineNum := i + 1
		if strings.Contains(line, "////") {
			continue
		}
		var auto bool
		m := errAutoRx.FindStringSubmatch(line)
		if m != nil {
			auto = true
		} else {
			m = errRx.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		mm := errQuotesRx.FindAllStringSubmatch(m[1], -1)
		if mm == nil {
			return nil, fmt.Errorf("%s:%d: invalid errchk line: %s", short, lineNum, line)
		}
		for _, m := range mm {
			rx := lineRx.ReplaceAllStringFunc(m[1], func(m string) string {
				n := lineNum
				if strings.HasPrefix(m, "LINE+") {
					delta, _ := strconv.Atoi(m[5:])
					n += delta
				} else if strings.HasPrefix(m, "LINE-") {
					delta, _ := strconv.Atoi(m[5:])
					n -= delta
				}
				return fmt.Sprintf("%s:%d", short, n)
			})
			re, err := regexp.Compile(rx)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: invalid regexp %q in ERROR line: %v", short, lineNum, rx, err)
			}
			wants = append(wants, Want{Line: lineNum, ReStr: rx, Re: re, Auto: auto})
		}
	}
	return wants, nil
}

// SplitOutput folds "\t"-prefixed continuation lines into the previous
// diagnostic and drops blank lines, as upstream splitOutput does.
func SplitOutput(out string) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "\t") && len(res) > 0 {
			res[len(res)-1] += "\n" + line
		} else if strings.TrimSpace(line) != "" {
			res = append(res, line)
		}
	}
	return res
}

// LineOf extracts the line number from "file:line:col: msg" ("" → 0).
func LineOf(raw string) int {
	// file may contain '/', never ':'; the first ':' ends it.
	i := strings.Index(raw, ":")
	if i < 0 {
		return 0
	}
	rest := raw[i+1:]
	j := strings.IndexAny(rest, ":[")
	if j < 0 {
		return 0
	}
	n, _ := strconv.Atoi(rest[:j])
	return n
}

// Classify runs the upstream comparison over diags and reports every failure.
//
// Rules (from upstream errorCheck): for each expectation, all diagnostics
// whose prefix is "short:line" are taken; if none → "missing error"; each
// that matches the regex is consumed, the rest go back; if none matched →
// "no match". Every diagnostic left at the end is an "Unmatched Error".
//
// Audit-only refinements on top of that:
//   - missing: no diagnostic anywhere matches the regex. Stage is checker
//     when the file had no scanner/parser diagnostics at all (the parser
//     accepted; whatever is owed comes later), parser otherwise (recovery
//     after a syntax error diverged; gc would not have run its checker).
//   - position: an unmatched expectation whose regex matches a diagnostic
//     on another line; stage is that diagnostic's stage.
//   - wording: no diagnostic on the expected line matches; stage is the
//     stage of the first diagnostic on that line.
//   - multiplicity: an unmatched diagnostic left on a line that has an
//     expectation; extra: one left on a line with no expectation.
func Classify(root, header string, wants []Want, diags []Diag) Result {
	r := Result{Root: root, Header: header, NDiags: len(diags), NWants: len(wants)}
	hasSyntax, hasChecker := false, false
	for _, d := range diags {
		if d.Stage == StageChecker {
			hasChecker = true
		} else {
			hasSyntax = true
		}
	}
	r.CheckerAfterSyntax = hasSyntax && hasChecker
	wantLines := map[int]bool{}
	for _, w := range wants {
		wantLines[w.Line] = true
	}
	// A line whose expectation failed as wording already owns its
	// diagnostics; upstream would also list them as "Unmatched Errors", but
	// that is the same event, not a second one.
	wordingLines := map[int]bool{}
	out := append([]Diag(nil), diags...)
	for _, w := range wants {
		var matched, rest []Diag
		for _, d := range out {
			if !w.Auto && d.Line == w.Line {
				matched = append(matched, d)
			} else {
				rest = append(rest, d)
			}
		}
		out = rest
		if len(matched) == 0 {
			f := Failure{Class: ClassMissing, Line: w.Line, Want: w.ReStr}
			if hasSyntax {
				f.Stage = StageParser
			} else {
				f.Stage = StageChecker
			}
			for _, d := range diags {
				if w.Re.MatchString(d.Text()) {
					f = Failure{Stage: d.Stage, Class: ClassPosition, Line: w.Line, Want: w.ReStr, Got: d.Raw}
					break
				}
			}
			r.Failures = append(r.Failures, f)
			continue
		}
		ok := false
		for _, d := range matched {
			if w.Re.MatchString(d.Text()) {
				if ok {
					r.DupMatched++
				}
				ok = true
			} else {
				out = append(out, d)
			}
		}
		if !ok {
			wordingLines[w.Line] = true
			r.Failures = append(r.Failures, Failure{Stage: matched[0].Stage, Class: ClassWording, Line: w.Line, Want: w.ReStr, Got: matched[0].Raw})
		}
	}
	for _, d := range out {
		c := ClassExtra
		if wantLines[d.Line] {
			if wordingLines[d.Line] {
				continue
			}
			c = ClassMultiplicity
		}
		r.Failures = append(r.Failures, Failure{Stage: d.Stage, Class: c, Line: d.Line, Got: d.Raw})
	}
	sort.SliceStable(r.Failures, func(i, j int) bool {
		if r.Failures[i].Stage != r.Failures[j].Stage {
			return r.Failures[i].Stage < r.Failures[j].Stage
		}
		return r.Failures[i].Line < r.Failures[j].Line
	})
	return r
}

// go/scanner messages (Go 1.27.0 src/go/scanner/scanner.go), by prefix.
var scannerMsgs = []string{
	"illegal character", "illegal UTF-8 encoding", "illegal byte order mark",
	"comment not terminated", "escape sequence not terminated",
	"unknown escape sequence", "escape sequence is invalid Unicode code point",
	"rune literal not terminated", "string literal not terminated",
	"raw string literal not terminated", "illegal rune literal",
	"'_' must separate successive digits", "invalid digit", "exponent has no digits",
	"hexadecimal mantissa requires a 'p' exponent", "invalid radix point",
	"invalid line number", "invalid column number", "curly quotation mark",
	"exponent requires decimal mantissa", "exponent requires hexadecimal mantissa",
	"number has no digits",
}

// IsScannerMessage reports whether a go/scanner.Error text is one the scanner
// (not the parser) emits.
func IsScannerMessage(text string) bool {
	for _, m := range scannerMsgs {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// gc scanner messages (cmd/compile/internal/syntax/scanner.go), by substring.
var gcScannerMsgs = []string{
	"invalid character", "invalid identifier character", "invalid UTF-8 encoding",
	"comment not terminated", "escape sequence not terminated", "unknown escape",
	"escape is invalid Unicode code point", "rune literal not terminated",
	"string not terminated", "empty rune literal", "more than one character in rune literal",
	"'_' must separate successive digits", "invalid digit", "exponent has no digits",
	"hexadecimal mantissa requires a 'p' exponent", "invalid radix point",
	"invalid line number", "invalid column number", "byte order mark", "NUL",
	"exponent requires decimal mantissa", "exponent requires hexadecimal mantissa",
	"number has no digits",
}

// IsGcScannerMessage reports whether a gc syntax.Error text came from its scanner.
func IsGcScannerMessage(text string) bool {
	if strings.HasPrefix(text, "syntax error:") {
		return false
	}
	for _, m := range gcScannerMsgs {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// Counts tallies stage×class across results.
type Counts map[Stage]map[Class]int

func Tally(results []Result) Counts {
	c := Counts{}
	for _, r := range results {
		s, k := r.FirstStage(), r.FirstClass()
		if c[s] == nil {
			c[s] = map[Class]int{}
		}
		c[s][k]++
	}
	return c
}

// Table renders the stage×class count matrix as Markdown.
func (c Counts) Table() string {
	var b strings.Builder
	classes := []Class{ClassWording, ClassPosition, ClassMultiplicity, ClassExtra, ClassMissing, ClassPass}
	b.WriteString("| stage \\ class |")
	for _, k := range classes {
		fmt.Fprintf(&b, " %s |", k)
	}
	b.WriteString(" total |\n|---|")
	for range classes {
		b.WriteString("---|")
	}
	b.WriteString("---|\n")
	for _, s := range []Stage{StageScanner, StageParser, StageChecker, StagePass} {
		total := 0
		fmt.Fprintf(&b, "| %s |", s)
		for _, k := range classes {
			n := c[s][k]
			total += n
			fmt.Fprintf(&b, " %d |", n)
		}
		fmt.Fprintf(&b, " %d |\n", total)
	}
	return b.String()
}

// RootSpec is one corpus root to audit.
type RootSpec struct {
	Root   string // corpus-relative path
	Header string // first line of the root (the errorcheck directive)
	Short  string // file name diagnostics are keyed by (upstream: the base name; tmp__.go for errorcheckoutput)
	Src    []byte // the source actually compiled
}
