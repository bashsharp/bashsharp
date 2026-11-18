// Package grammar interprets the published Bash# delta GBNF as a parsing
// expression grammar. Every verdict comes from the productions in delta.gbnf;
// the only Go-coded nonterminal is the explicitly opaque base-Bash fragment.
package grammar

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OpaqueRules names the nonterminals the interpreter binds to Go code instead
// of their published (approximate) right-hand side. bash-fragment is ordinary
// Bash text with its comment, quote and heredoc boundaries. fence-lexeme is a
// lexical primitive: standard GBNF cannot retain an arbitrary opener width and
// require the first line with that exact width to be its closer.
var OpaqueRules = []string{"bash-fragment", "fence-lexeme"}

// ReportedRules are the delta-owned sites the interpreter records on success.
var ReportedRules = []string{
	"fence", "embed-line", "decorator", "at-function",
	"agentic-block", "agentic-shell-func", "agentic-typed-func", "agentic-typed-method",
	"typed-func", "typed-method", "shell-function",
}

type Grammar struct {
	rules map[string]node
	order []string
	refs  map[string][]string
}

// Site is one recognized delta-owned production.
type Site struct {
	Rule string
	Line int
}

// Load parses a GBNF file. A line starting with whitespace continues the
// previous rule.
func Load(path string) (*Grammar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	g := &Grammar{rules: map[string]node{}, refs: map[string][]string{}}
	var logical []string
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 1<<20), 1<<20)
	for scan.Scan() {
		raw := scan.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		if (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) && len(logical) > 0 {
			logical[len(logical)-1] += " " + strings.TrimSpace(raw)
			continue
		}
		logical = append(logical, strings.TrimSpace(raw))
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	for _, line := range logical {
		name, rhs, ok := strings.Cut(line, "::=")
		if !ok {
			return nil, fmt.Errorf("invalid GBNF rule: %s", line)
		}
		name = strings.TrimSpace(name)
		if name == "" || strings.TrimSpace(rhs) == "" {
			return nil, fmt.Errorf("empty GBNF rule: %s", line)
		}
		if _, exists := g.rules[name]; exists {
			return nil, fmt.Errorf("duplicate GBNF rule %s", name)
		}
		p := &gbnfParser{src: strings.TrimSpace(rhs)}
		n, err := p.choice()
		if err == nil {
			p.skip()
			if p.i != len(p.src) {
				err = fmt.Errorf("unexpected %q", p.src[p.i:])
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		g.rules[name] = n
		g.order = append(g.order, name)
		g.refs[name] = p.refs
	}
	for name, refs := range g.refs {
		for _, r := range refs {
			if _, ok := g.rules[r]; !ok {
				return nil, fmt.Errorf("%s: undefined GBNF rule %s", name, r)
			}
		}
	}
	for _, name := range []string{"root"} {
		if _, ok := g.rules[name]; !ok {
			return nil, fmt.Errorf("missing GBNF rule %s", name)
		}
	}
	for _, name := range OpaqueRules {
		if _, ok := g.rules[name]; !ok {
			return nil, fmt.Errorf("opaque rule %s is not published", name)
		}
	}
	return g, nil
}

// Rules lists the published rule names in file order.
func (g *Grammar) Rules() []string { return append([]string(nil), g.order...) }

// References lists the nonterminals a rule's right-hand side mentions.
func (g *Grammar) References(rule string) []string {
	out := append([]string(nil), g.refs[rule]...)
	sort.Strings(out)
	return out
}

// Match reports whether rule matches the entire source.
func (g *Grammar) Match(rule, source string) bool {
	n, ok := g.rules[rule]
	if !ok {
		return false
	}
	for _, state := range matchAll(n, matcher{g: g, src: source}, 0) {
		if state.pos == len(source) {
			return true
		}
	}
	return false
}

// matchAt reports the end offset of rule matched as a prefix of src[pos:].
func (g *Grammar) matchAt(rule, src string, pos int) (int, bool) {
	m := &matcher{g: g, src: src}
	return g.rules[rule].match(m, pos)
}

// CheckDelta interprets root over a whole program.
func (g *Grammar) CheckDelta(src string) error {
	_, err := g.Inspect("", src)
	return err
}

// CheckDeltaFile is CheckDelta with the program's path, so an embed path is
// resolved the way the engine resolves it.
func (g *Grammar) CheckDeltaFile(name, src string) error {
	_, err := g.Inspect(name, src)
	return err
}

// Inspect runs root over a newline-terminated copy of src and returns the
// delta-owned sites it recognized. A missing embed target is reported after
// recognition, exactly as the engine does.
func (g *Grammar) Inspect(name, src string) ([]Site, error) {
	if src != "" && !strings.HasSuffix(src, "\n") {
		src += "\n"
	}
	states := matchAll(g.rules["root"], matcher{g: g, src: src}, 0)
	var m matcher
	end, ok := 0, false
	for _, state := range states {
		if state.pos == len(src) {
			m, end, ok = state.matcher, state.pos, true
			break
		}
	}
	sites := make([]Site, 0, len(m.trail))
	for _, t := range m.trail {
		if nestedSite(m.trail, t) {
			continue
		}
		sites = append(sites, Site{Rule: t.rule, Line: lineOf(src, t.pos)})
	}
	if !ok || end != len(src) {
		far := max(m.farthest, end)
		return sites, fmt.Errorf("line %d: no delta production accepts %q", lineOf(src, far), lineAt(src, far))
	}
	for _, t := range m.trail {
		if t.rule != "embed-line" {
			continue
		}
		rel := embedPath(src[t.pos:t.end])
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(name), path)
		}
		if _, err := os.Stat(path); err != nil {
			return sites, fmt.Errorf("line %d: embed: %v", lineOf(src, t.pos), err)
		}
	}
	return sites, nil
}

// nestedSite reports a typed declaration that is the body of an agentic
// modifier production; the modifier form is the site the engine reports.
func nestedSite(trail []span, t span) bool {
	if t.rule != "typed-func" && t.rule != "typed-method" {
		return false
	}
	for _, o := range trail {
		if o.rule == "agentic-"+t.rule && o.end == t.end && o.pos < t.pos {
			return true
		}
	}
	return false
}

// Counts folds sites into per-rule totals.
func Counts(sites []Site) map[string]int {
	out := map[string]int{}
	for _, s := range sites {
		out[s.Rule]++
	}
	return out
}

func embedPath(line string) string {
	i := strings.IndexByte(line, '"')
	j := strings.IndexByte(line[i+1:], '"')
	return line[i+1 : i+1+j]
}

func lineOf(src string, pos int) int {
	if pos > len(src) {
		pos = len(src)
	}
	return strings.Count(src[:pos], "\n") + 1
}

func lineAt(src string, pos int) string {
	if pos > len(src) {
		pos = len(src)
	}
	start := strings.LastIndexByte(src[:pos], '\n') + 1
	end := strings.IndexByte(src[start:], '\n')
	if end < 0 {
		return src[start:]
	}
	return src[start : start+end]
}

// ---- GBNF syntax ----

type gbnfParser struct {
	src  string
	i    int
	refs []string
}

func (p *gbnfParser) skip() bool {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t') {
		p.i++
	}
	return p.i < len(p.src)
}

func (p *gbnfParser) choice() (node, error) {
	var parts []node
	for {
		s, err := p.sequence()
		if err != nil {
			return nil, err
		}
		parts = append(parts, s)
		if !p.skip() || p.src[p.i] != '|' {
			break
		}
		p.i++
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return alt(parts), nil
}

func (p *gbnfParser) sequence() (node, error) {
	var items []node
	for p.skip() && p.src[p.i] != ')' && p.src[p.i] != '|' {
		atom, err := p.atom()
		if err != nil {
			return nil, err
		}
		if p.skip() && strings.ContainsRune("*+?", rune(p.src[p.i])) {
			switch p.src[p.i] {
			case '*':
				atom = &rep{n: atom, min: 0, max: -1}
			case '+':
				atom = &rep{n: atom, min: 1, max: -1}
			case '?':
				atom = &rep{n: atom, min: 0, max: 1}
			}
			p.i++
		}
		items = append(items, atom)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return seq(items), nil
}

func (p *gbnfParser) atom() (node, error) {
	switch p.src[p.i] {
	case '(':
		p.i++
		s, err := p.choice()
		if err != nil {
			return nil, err
		}
		if !p.skip() || p.src[p.i] != ')' {
			return nil, fmt.Errorf("unclosed group")
		}
		p.i++
		return s, nil
	case '"':
		p.i++
		var b strings.Builder
		for p.i < len(p.src) && p.src[p.i] != '"' {
			c, err := p.escaped()
			if err != nil {
				return nil, err
			}
			b.WriteByte(c)
		}
		if p.i >= len(p.src) {
			return nil, fmt.Errorf("unclosed string")
		}
		p.i++
		return lit(b.String()), nil
	case '[':
		p.i++
		c := &class{}
		if p.i < len(p.src) && p.src[p.i] == '^' {
			c.neg = true
			p.i++
		}
		for p.i < len(p.src) && p.src[p.i] != ']' {
			lo, err := p.escaped()
			if err != nil {
				return nil, err
			}
			hi := lo
			if p.i+1 < len(p.src) && p.src[p.i] == '-' && p.src[p.i+1] != ']' {
				p.i++
				if hi, err = p.escaped(); err != nil {
					return nil, err
				}
			}
			c.ranges = append(c.ranges, [2]byte{lo, hi})
		}
		if p.i >= len(p.src) {
			return nil, fmt.Errorf("unclosed character class")
		}
		p.i++
		return c, nil
	default:
		start := p.i
		for p.i < len(p.src) {
			c := p.src[p.i]
			if c == '-' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
				p.i++
			} else {
				break
			}
		}
		if start == p.i {
			return nil, fmt.Errorf("unexpected %q", p.src[p.i:])
		}
		name := p.src[start:p.i]
		p.refs = append(p.refs, name)
		return ref(name), nil
	}
}

func (p *gbnfParser) escaped() (byte, error) {
	c := p.src[p.i]
	p.i++
	if c != '\\' {
		return c, nil
	}
	if p.i >= len(p.src) {
		return 0, fmt.Errorf("dangling escape")
	}
	e := p.src[p.i]
	p.i++
	switch e {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	case 'r':
		return '\r', nil
	default:
		return e, nil
	}
}

// ---- PEG interpreter ----

type matcher struct {
	g        *Grammar
	src      string
	trail    []span
	farthest int
	depth    int
}

type span struct {
	rule     string
	pos, end int
}

type node interface {
	match(m *matcher, pos int) (int, bool)
}

type matchState struct {
	matcher matcher
	pos     int
}

// matchAll preserves the published PEG alternative order while retaining every
// successful repetition/recursive path. The caller can therefore backtrack a
// recursive production when a later token rejects an otherwise valid prefix.
// This is needed by fence-pair: a shorter tilde run can be body text when an
// outer pair still needs its matching closer. No grammar rule is interpreted
// specially here.
func matchAll(n node, m matcher, pos int) []matchState {
	clone := func(m matcher) matcher { m.trail = append([]span(nil), m.trail...); return m }
	switch n := n.(type) {
	case lit, *class:
		m = clone(m)
		if end, ok := n.match(&m, pos); ok {
			return []matchState{{m, end}}
		}
	case ref:
		name := string(n)
		m = clone(m)
		if hook := hooks[name]; hook != nil {
			if end, ok := hook(&m, pos); ok {
				return []matchState{{m, end}}
			}
			return nil
		}
		if m.depth >= 20000 {
			return nil
		}
		m.depth++
		out := matchAll(m.g.rules[name], m, pos)
		for i := range out {
			out[i].matcher.depth--
			if reported[name] {
				out[i].matcher.trail = append(out[i].matcher.trail, span{rule: name, pos: pos, end: out[i].pos})
			}
		}
		return out
	case seq:
		states := []matchState{{clone(m), pos}}
		for _, child := range n {
			var next []matchState
			for _, state := range states {
				next = append(next, matchAll(child, state.matcher, state.pos)...)
			}
			states = next
			if len(states) == 0 {
				return nil
			}
		}
		return states
	case alt:
		var out []matchState
		for _, child := range n {
			out = append(out, matchAll(child, m, pos)...)
		}
		return out
	case *rep:
		levels := [][]matchState{{{clone(m), pos}}}
		for count := 0; n.max < 0 || count < n.max; count++ {
			var next []matchState
			for _, state := range levels[len(levels)-1] {
				for _, candidate := range matchAll(n.n, state.matcher, state.pos) {
					if candidate.pos != state.pos {
						next = append(next, candidate)
					}
				}
			}
			if len(next) == 0 {
				break
			}
			levels = append(levels, next)
		}
		var out []matchState
		for count := len(levels) - 1; count >= n.min; count-- {
			out = append(out, levels[count]...)
		}
		return out
	}
	return nil
}

type lit string

func (l lit) match(m *matcher, pos int) (int, bool) {
	if strings.HasPrefix(m.src[pos:], string(l)) {
		return pos + len(l), true
	}
	return pos, false
}

type class struct {
	neg    bool
	ranges [][2]byte
}

func (c *class) match(m *matcher, pos int) (int, bool) {
	if pos >= len(m.src) {
		return pos, false
	}
	b := m.src[pos]
	in := false
	for _, r := range c.ranges {
		if b >= r[0] && b <= r[1] {
			in = true
			break
		}
	}
	if in != c.neg {
		return pos + 1, true
	}
	return pos, false
}

type ref string

func (r ref) match(m *matcher, pos int) (int, bool) {
	name := string(r)
	if hook := hooks[name]; hook != nil {
		return hook(m, pos)
	}
	m.depth++
	if m.depth > 20000 {
		m.depth--
		return pos, false
	}
	mark := len(m.trail)
	end, ok := m.g.rules[name].match(m, pos)
	m.depth--
	if !ok {
		m.trail = m.trail[:mark]
		return pos, false
	}
	if reported[name] {
		m.trail = append(m.trail, span{rule: name, pos: pos, end: end})
	}
	return end, true
}

type seq []node

func (s seq) match(m *matcher, pos int) (int, bool) {
	mark := len(m.trail)
	cur := pos
	for _, n := range s {
		end, ok := n.match(m, cur)
		if !ok {
			m.trail = m.trail[:mark]
			if cur > m.farthest {
				m.farthest = cur
			}
			return pos, false
		}
		cur = end
	}
	return cur, true
}

type alt []node

func (a alt) match(m *matcher, pos int) (int, bool) {
	for _, n := range a {
		mark := len(m.trail)
		if end, ok := n.match(m, pos); ok {
			return end, true
		}
		m.trail = m.trail[:mark]
	}
	return pos, false
}

type rep struct {
	n        node
	min, max int
}

func (r *rep) match(m *matcher, pos int) (int, bool) {
	cur, count := pos, 0
	for r.max < 0 || count < r.max {
		mark := len(m.trail)
		end, ok := r.n.match(m, cur)
		if !ok || end == cur {
			m.trail = m.trail[:mark]
			break
		}
		cur = end
		count++
	}
	if count < r.min {
		return pos, false
	}
	return cur, true
}

var reported = func() map[string]bool {
	out := map[string]bool{}
	for _, r := range ReportedRules {
		out[r] = true
	}
	return out
}()

var hooks = map[string]func(m *matcher, pos int) (int, bool){
	"bash-fragment": bashFragment,
	"fence-lexeme":  fenceLexeme,
}

// fenceLexeme consumes a whole fence using the first exact-width closing line.
// Its header is still the published fence-open rule; this hook only carries the
// opener width into the body scan, a comparison that ordinary GBNF cannot
// express for an unbounded delimiter. A closer is exactly the delimiter plus a
// newline: spaces or a longer/shorter tilde run remain body bytes.
func fenceLexeme(m *matcher, pos int) (int, bool) {
	openEnd, ok := m.g.matchAt("fence-open", m.src, pos)
	if !ok {
		return pos, false
	}
	delimEnd := pos
	for delimEnd < len(m.src) && m.src[delimEnd] == '~' {
		delimEnd++
	}
	delim := m.src[pos:delimEnd]
	for i := openEnd; i < len(m.src); {
		end := strings.IndexByte(m.src[i:], '\n')
		if end < 0 {
			return pos, false
		}
		end += i
		if m.src[i:end] == delim {
			return end + 1, true
		}
		i = end + 1
	}
	return pos, false
}

// ---- opaque base-Bash fragment ----

// bashFragment consumes ordinary Bash text up to the next point the delta
// grammar owns: a bare `{` or `}` word, a column-one fence header or embed
// line, a line-leading decorator-shaped word, or a line-leading `func` or
// `agentic` word. It honors comments, single and double quotes, backslash
// continuation and heredoc bodies, so none of those boundaries open a delta
// production. Command substitutions are not tracked: the engine recognizes a
// column-one fence inside `$( )` and backticks, and so does this scanner.
func bashFragment(m *matcher, pos int) (int, bool) {
	src := m.src
	i := pos
	litDepth := 0
	continued := -2
	var heredocs []heredoc
	for i < len(src) {
		atLineStart := (i == 0 || src[i-1] == '\n') && i-1 != continued
		if atLineStart {
			if _, ok := m.g.matchAt("fence-open", src, i); ok {
				return i, true
			}
			if _, ok := m.g.matchAt("embed-line", src, i); ok {
				return i, true
			}
			j := i
			for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
			if _, ok := m.g.matchAt("decorator-head", src, j); ok {
				return i, true
			}
			if leadingKeyword(src, j) {
				return j, true
			}
		}
		c := src[i]
		switch {
		case c == '\\':
			if i+1 < len(src) && src[i+1] == '\n' {
				continued = i + 1
			}
			i += 2
			continue
		case c == '\'':
			end := strings.IndexByte(src[i+1:], '\'')
			if end < 0 {
				return len(src), true
			}
			i += end + 2
			continue
		case c == '"':
			i++
			for i < len(src) && src[i] != '"' {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		case c == '#' && wordStart(src, i):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '<' && strings.HasPrefix(src[i:], "<<") && !strings.HasPrefix(src[i:], "<<<") && !(i > 0 && src[i-1] == '<'):
			if h, next, ok := heredocWord(src, i+2); ok {
				heredocs = append(heredocs, h)
				i = next
				continue
			}
			i += 2
			continue
		case c == '\n':
			i++
			for _, h := range heredocs {
				i = skipHeredoc(src, i, h)
			}
			heredocs = nil
			continue
		case c == '{':
			switch {
			case !standaloneWord(src, i):
				litDepth++ // attached, as in `T{` or `[]int{`: a literal with a partner
			case litDepth == 0 && bareOpen(src, i):
				return i, true
			}
			// A standalone `{` in argument position is a plain word.
		case c == '}':
			if litDepth > 0 {
				litDepth--
			} else if bareClose(src, i) {
				return i, true
			}
		}
		i++
	}
	return len(src), true
}

type heredoc struct {
	delim     string
	stripTabs bool
}

func heredocWord(src string, i int) (heredoc, int, bool) {
	h := heredoc{}
	if i < len(src) && src[i] == '-' {
		h.stripTabs = true
		i++
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	var b strings.Builder
	quote := byte(0)
	for i < len(src) {
		c := src[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			i++
			continue
		}
		if c == '\\' && i+1 < len(src) {
			b.WriteByte(src[i+1])
			i += 2
			continue
		}
		if strings.IndexByte(" \t\n;|&<>()", c) >= 0 {
			break
		}
		b.WriteByte(c)
		i++
	}
	h.delim = b.String()
	return h, i, h.delim != ""
}

func skipHeredoc(src string, i int, h heredoc) int {
	for i < len(src) {
		end := strings.IndexByte(src[i:], '\n')
		line := src[i:]
		next := len(src)
		if end >= 0 {
			line = src[i : i+end]
			next = i + end + 1
		}
		if h.stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		i = next
		if line == h.delim {
			return i
		}
	}
	return i
}

func wordStart(src string, i int) bool {
	return i == 0 || strings.IndexByte(" \t\n;(&|", src[i-1]) >= 0
}

// blockKeywords are the Bash# statement heads after which a trailing bare
// `{` opens a block rather than being an argument word.
var blockKeywords = map[string]bool{
	"if": true, "else": true, "elif": true, "for": true, "switch": true, "select": true,
	"type": true, "var": true, "const": true, "go": true, "defer": true,
	"func": true, "function": true, "while": true, "until": true, "then": true, "do": true,
}

// commandHead returns the first word of the command containing offset i and
// whether i itself is at command position.
func commandHead(src string, i int) (string, bool) {
	j := i
	for j > 0 && (src[j-1] == ' ' || src[j-1] == '\t') {
		j--
	}
	if j > 0 && src[j-1] == ')' { // `func() {`, `f() {`, `for (...) {`
		return "func", false
	}
	// Go-style headers carry `;` (`for i := 0; i < n; i++ {`), so the head
	// is the first word of the line or of the enclosing brace or paren.
	start := j
	for start > 0 && strings.IndexByte("\n({}", src[start-1]) < 0 {
		start--
	}
	fields := strings.Fields(src[start:j])
	if len(fields) == 0 {
		return "", true
	}
	return fields[0], false
}

func standaloneWord(src string, i int) bool {
	before := i == 0 || strings.IndexByte(" \t\n;(&|", src[i-1]) >= 0
	after := i+1 >= len(src) || strings.IndexByte(" \t\n;}", src[i+1]) >= 0
	return before && after
}

func bareOpen(src string, i int) bool {
	head, atCommand := commandHead(src, i)
	return atCommand || blockKeywords[head] || strings.HasSuffix(head, ")")
}

// bareClose accepts a `}` word wherever Bash# does: after whitespace, a
// separator or an opening brace, and before whitespace, a separator or the
// end of input. Bash# bodies close blocks without a preceding `;`.
func bareClose(src string, i int) bool {
	before := i == 0 || strings.IndexByte(" \t\n;{", src[i-1]) >= 0
	after := i+1 >= len(src) || strings.IndexByte(" \t\n;)&|", src[i+1]) >= 0
	return before && after
}

func leadingKeyword(src string, j int) bool {
	for _, kw := range []string{"func", "agentic"} {
		if strings.HasPrefix(src[j:], kw) {
			k := j + len(kw)
			if k >= len(src) || strings.IndexByte(" \t\n;", src[k]) >= 0 {
				return true
			}
		}
	}
	return false
}
