// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pattern"
)

func indexedNegativeOffset(vr Variable, n int) int {
	indexes := vr.IndexedIndexes()
	if len(indexes) == 0 {
		return n
	}
	return indexes[len(indexes)-1] + 1 + n
}

// stripBackslashEscapes removes a single backslash before any
// character, mirroring bash 5.3's quote-removal pass on the replacement
// in ${var/pat/repl}: `\X` → `X` for any X, `\\` → `\`. A trailing
// backslash is kept as-is.
func stripBackslashEscapes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// starSliceQuotedNull reports whether word is an unquoted substring slice
// of a `*`-form parameter expansion: `${*:off}` or `${name[*]:off}` (for
// an indexed or associative array). bash joins such a slice using IFS, and
// any literal 0x7f (DEL) bytes in the result become "quoted nulls" that are
// removed when the value is consumed in an unquoted assignment context
// (`var=…`, `${var=…}`). The `@`-form (`${@:off}`) and a plain literal
// like `$'\177'` keep their 0x7f instead — see expand's dollar-at-star9
// fidelity case.
func starSliceQuotedNull(word *syntax.Word) bool {
	if word == nil || len(word.Parts) != 1 {
		return false
	}
	pe, ok := word.Parts[0].(*syntax.ParamExp)
	if !ok || pe.Slice == nil || pe.Excl {
		return false
	}
	if pe.Param != nil && pe.Param.Value == "*" && pe.Index == nil {
		return true
	}
	return nodeLit(pe.Index) == "*"
}

func stripQuotedNulls(s string) string {
	if !strings.ContainsRune(s, 0x7f) {
		return s
	}
	return strings.ReplaceAll(s, "\x7f", "")
}

func stripParamExpLitEscapes(s string, stripSingle bool) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"', '\\', '$', '`', '}':
				b.WriteByte(s[i+1])
				i++
				continue
			case '\'':
				if stripSingle {
					b.WriteByte(s[i+1])
					i++
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (cfg *Config) literalParamExpWord(word *syntax.Word, innerDoubleQuoted bool) (string, error) {
	if word == nil {
		return "", nil
	}
	var sb strings.Builder
	for i, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if cfg.insideDoubleQuote {
				val, err := cfg.literalParamExpQuotedText(part.Value)
				if err != nil {
					return "", err
				}
				sb.WriteByte('\'')
				sb.WriteString(val)
				sb.WriteByte('\'')
			} else {
				sb.WriteString(part.Value)
			}
		case *syntax.DblQuoted:
			val, err := cfg.literalParamExpWord(&syntax.Word{Parts: part.Parts}, true)
			if err != nil {
				return "", err
			}
			sb.WriteString(val)
		case *syntax.Lit:
			val := part.Value
			// Tilde-expand a leading unquoted `~`/`~user` in an alternate
			// word, e.g. `${x:+~/path}`, matching wordField's expandUser (the
			// `:-` default word already does this via Literal). Guarded to the
			// first part of a genuinely unquoted word so the other callers
			// (double-quoted contexts) are unaffected.
			if i == 0 && !cfg.insideDoubleQuote && !innerDoubleQuoted {
				if prefix, rest := cfg.expandUser(val, len(word.Parts) > 1); prefix != "" {
					val = prefix + rest
				}
			}
			if innerDoubleQuoted {
				if cfg.insideDoubleQuote {
					sb.WriteString(stripBackslashEscapes(val))
				} else {
					sb.WriteString(stripParamExpLitEscapes(val, false))
				}
			} else if !cfg.insideDoubleQuote {
				sb.WriteString(stripBackslashEscapes(val))
			} else {
				sb.WriteString(stripParamExpLitEscapes(val, false))
			}
		default:
			val, err := Literal(cfg, &syntax.Word{Parts: []syntax.WordPart{part}})
			if err != nil {
				return "", err
			}
			sb.WriteString(val)
		}
	}
	return sb.String(), nil
}

func (cfg *Config) literalParamExpQuotedText(text string) (string, error) {
	if !strings.ContainsAny(text, "$`\\") {
		return text, nil
	}
	word, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Document(strings.NewReader(text))
	if err != nil && err != io.EOF {
		return "", err
	}
	return Literal(cfg, word)
}

func paramExpWordSingleQuotesOnly(word *syntax.Word) bool {
	if word == nil || len(word.Parts) == 0 {
		return false
	}
	for _, part := range word.Parts {
		sq, ok := part.(*syntax.SglQuoted)
		if !ok || sq.Dollar {
			return false
		}
	}
	return true
}

func paramExpWordHasBackslashLit(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsRune(part.Value, '\\') {
				return true
			}
		case *syntax.DblQuoted:
			if paramExpWordHasBackslashLit(&syntax.Word{Parts: part.Parts}) {
				return true
			}
		}
	}
	return false
}

func paramExpWordHasSingleQuotedLeadingDouble(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		sq, ok := part.(*syntax.SglQuoted)
		if ok && !sq.Dollar && strings.HasPrefix(sq.Value, `"`) {
			return true
		}
	}
	return false
}

func (cfg *Config) literalParamExpDoubleQuotedDefault(word *syntax.Word) (string, error) {
	var sb strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if !part.Dollar && strings.HasPrefix(part.Value, `"`) {
				sb.WriteByte('\'')
				sb.WriteString(strings.TrimPrefix(part.Value, `"`))
				sb.WriteByte('\'')
				continue
			}
		}
		val, err := cfg.literalParamExpWord(&syntax.Word{Parts: []syntax.WordPart{part}}, false)
		if err != nil {
			return "", err
		}
		sb.WriteString(val)
	}
	return sb.String(), nil
}

func paramExpDefaultTriggers(op syntax.ParExpOperator, vr Variable, str string) bool {
	switch op {
	case syntax.DefaultUnset:
		return !vr.IsSet()
	case syntax.DefaultUnsetOrNull:
		return !vr.IsSet() || str == ""
	}
	return false
}

// paramIsSetNonColon reports whether the parameter counts as "set" for the
// non-colon ${var-w} / ${var+w} tests. Bash treats an empty indexed or
// associative array as UNSET here: with no element[0] there is no value to
// address, so `a=(); ${a-w}` and `${a[@]+w}` behave as if `a` were never set.
// (The colon forms `:-`/`:+` test the expanded string instead and are
// unaffected.) Positional parameters ($@/$*) use the parameter's aggregate
// set state; a specific subscript tests the addressed array element.
func paramIsSetNonColon(cfg *Config, vr Variable, name string, index syntax.ArithmExpr) bool {
	if name == "@" || name == "*" {
		return vr.IsSet()
	}
	if index == nil {
		// A scalar reference addresses element[0] (`${a}` == `${a[0]}`),
		// so an array without that element is unset for this test.
		switch vr.Kind {
		case Indexed:
			return vr.IndexedSet(0)
		case Associative:
			_, ok := vr.Map["0"]
			return ok
		}
		return vr.IsSet()
	}
	switch nodeLit(index) {
	case "@", "*":
		// Whole-array reference: set iff the array has any element.
		switch vr.Kind {
		case Indexed:
			return vr.IndexedCount() > 0
		case Associative:
			return len(vr.Map) > 0
		}
	default:
		// A specific subscript (numeric, dynamic `$i`, or empty) addresses an
		// array element; arrayElemSet evaluates the index and tests it. (For a
		// scalar, `${s[0]}` == `${s}` — arrayElemSet handles that too.)
		return arrayElemSet(vr, index, cfg)
	}
	return vr.IsSet()
}

func indirectDefaultOp(op syntax.ParExpOperator) bool {
	switch op {
	case syntax.AlternateUnset, syntax.AlternateUnsetOrNull,
		syntax.DefaultUnset, syntax.DefaultUnsetOrNull,
		syntax.ErrorUnset, syntax.ErrorUnsetOrNull:
		return true
	}
	return false
}

func indirectAtOp(op syntax.ParExpOperator) bool {
	return op == syntax.OtherParamOps
}

func transformNounsetUnset(vr Variable) bool {
	return !vr.IsSet() || vr.Kind == Indexed && vr.IndexedCount() == 0
}

func indirectParamModOp(op syntax.ParExpOperator) bool {
	switch op {
	case syntax.RemSmallPrefix, syntax.RemLargePrefix,
		syntax.RemSmallSuffix, syntax.RemLargeSuffix:
		return true
	}
	return false
}

func nodeLit(node syntax.Node) string {
	if word, ok := node.(*syntax.Word); ok {
		return word.Lit()
	}
	return ""
}

func (cfg *Config) cLocale() bool {
	if cfg == nil || cfg.Env == nil {
		return false
	}
	locale := cfg.envGet("LC_ALL")
	if locale == "" {
		locale = cfg.envGet("LC_CTYPE")
	}
	if locale == "" {
		locale = cfg.envGet("LANG")
	}
	return locale == "C" || locale == "POSIX"
}

// localeCategory resolves the named LC_* category following bash's
// LC_ALL > category > LANG precedence (mirrors interp.Runner.localeCategory).
func (cfg *Config) localeCategory(category string) string {
	if cfg == nil || cfg.Env == nil {
		return ""
	}
	if v := cfg.envGet("LC_ALL"); v != "" {
		return v
	}
	if v := cfg.envGet(category); v != "" {
		return v
	}
	return cfg.envGet("LANG")
}

// localePatternMode reports whether LC_COLLATE or LC_CTYPE names a locale
// other than C/POSIX (or unset), in which case pattern matching should
// honor non-C character classes and equivalence classes ([pattern.Locale]).
func (cfg *Config) localePatternMode() bool {
	nonC := func(locale string) bool {
		switch strings.ToLower(locale) {
		case "", "c", "posix":
			return false
		}
		return true
	}
	return nonC(cfg.localeCategory("LC_COLLATE")) || nonC(cfg.localeCategory("LC_CTYPE"))
}

func (cfg *Config) paramStringLen(s string) int {
	if cfg.cLocale() {
		return len(s)
	}
	return utf8.RuneCountInString(s)
}

func bashToUpper(r rune) rune {
	if r == 'ß' {
		return 'ẞ'
	}
	return unicode.ToUpper(r)
}

func bashToUpperString(s string) string {
	return strings.Map(bashToUpper, s)
}

// arrayElemSet reports whether a specific array element is set.
// For indexed arrays, it checks the ListSet map. For associative arrays,
// it checks if the key exists in the Map. Returns true for scalar variables.
func arrayElemSet(vr Variable, idx syntax.ArithmExpr, cfg *Config) bool {
	if idx == nil {
		return vr.IsSet()
	}
	switch vr.Kind {
	case Associative:
		key, err := assocSubscriptKey(cfg, idx)
		if err != nil {
			return false
		}
		_, ok := vr.Map[key]
		return ok
	case Indexed:
		i, err := Arithm(cfg, indexedQuotedLiteralIndex(idx))
		if err != nil {
			return false
		}
		if i < 0 {
			// Handle negative index
			indexes := vr.IndexedIndexes()
			if len(indexes) == 0 {
				return false
			}
			i = indexes[len(indexes)-1] + 1 + i
			if i < 0 {
				return false
			}
		}
		return vr.IndexedSet(i)
	default:
		return vr.IsSet()
	}
}

// envSetIndex sets a variable value, handling array element assignment when
// idx is not nil. Used by parameter expansion assignment operators like ${a[i]=value}.
// integerValue evaluates value as an arithmetic expression, returning its
// decimal string form. It mirrors the `declare -i` assignment path in interp:
// an empty value is zero, and the result replaces the literal text so that
// e.g. `a[42]=4+3` on an integer array stores `7`.
func (cfg *Config) integerValue(value string) (string, error) {
	if value == "" {
		return "0", nil
	}
	expr, err := syntax.NewParser().Arithmetic(strings.NewReader(value))
	if err != nil || expr == nil {
		return value, err
	}
	n, err := Arithm(cfg, expr)
	if err != nil {
		return value, err
	}
	return strconv.Itoa(n), nil
}

// foldCaseAttr applies a variable's `declare -u`/`-l`/`-c` case-folding
// attribute to an assigned value, matching interp's applyCaseAttr.
func foldCaseAttr(vr Variable, value string) string {
	switch {
	case vr.Upper:
		return strings.ToUpper(value)
	case vr.Lower:
		return strings.ToLower(value)
	case vr.Capitalize:
		if value == "" {
			return value
		}
		rs := []rune(value)
		rs[0] = unicode.ToUpper(rs[0])
		for i := 1; i < len(rs); i++ {
			rs[i] = unicode.ToLower(rs[i])
		}
		return string(rs)
	}
	return value
}

func (cfg *Config) envSetIndex(name string, idx syntax.ArithmExpr, value string) error {
	wenv, ok := cfg.Env.(WriteEnviron)
	if !ok {
		return fmt.Errorf("environment is read-only")
	}
	if idx == nil {
		return cfg.envSet(name, value)
	}

	vr := cfg.Env.Get(name)

	// An integer-attributed array (`declare -ai`/`declare -Ai`) evaluates
	// each assigned element value as an arithmetic expression.
	if vr.Integer {
		v, err := cfg.integerValue(value)
		if err != nil {
			return err
		}
		value = v
	}

	// Check for special indices. `@` and `*` cannot be assigned to an
	// indexed array or scalar, but for an associative array they are
	// ordinary literal keys (`${A[@]:=v}` sets key `@`).
	switch nodeLit(idx) {
	case "@", "*":
		if vr.Kind != Associative {
			return fmt.Errorf("%s: cannot assign in this way", name)
		}
		if vr.Map == nil {
			vr.Map = make(map[string]string)
		} else {
			vr.Map = maps.Clone(vr.Map)
		}
		vr.Set = true
		vr.Kind = Associative
		vr.Map[nodeLit(idx)] = value
		return wenv.Set(name, vr)
	}

	if vr.Kind == Associative {
		// Associative array: evaluate index as string
		key, err := Literal(cfg, idx.(*syntax.Word))
		if err != nil {
			return err
		}
		if vr.Map == nil {
			vr.Map = make(map[string]string)
		} else {
			vr.Map = maps.Clone(vr.Map)
		}
		vr.Set = true
		vr.Kind = Associative
		vr.Map[key] = value
		return wenv.Set(name, vr)
	}

	// Indexed array: evaluate index as arithmetic
	i, err := Arithm(cfg, idx)
	if err != nil {
		return err
	}
	if i < 0 {
		// Handle negative index - convert to positive offset from end
		indexes := vr.IndexedIndexes()
		if len(indexes) == 0 {
			i = 0
		} else {
			i = indexes[len(indexes)-1] + 1 + i
			if i < 0 {
				return fmt.Errorf("%s: bad array subscript", name)
			}
		}
	}

	// Convert string to indexed array if needed
	if vr.Kind == String && vr.Str != "" {
		vr.List = []string{vr.Str}
		vr.ListSet = nil
	}

	vr.Kind = Indexed
	vr.Set = true
	vr.List = slices.Clone(vr.List)
	vr.ListSet = vr.CloneListSet()
	vr.ListMap = vr.CloneListMap()
	vr.SetIndexed(i, value)

	return wenv.Set(name, vr)
}

// UnsetParameterError is returned when a parameter expansion encounters an
// unset variable and [Config.NoUnset] has been set.
type UnsetParameterError struct {
	Node    *syntax.ParamExp
	Name    string
	Message string
}

func (u UnsetParameterError) Error() string {
	name := u.Name
	if name == "" && u.Node != nil && u.Node.Param != nil {
		name = u.Node.Param.Value
		if u.Node.Short {
			numeric := true
			for i := 0; i < len(name); i++ {
				if name[i] < '0' || name[i] > '9' {
					numeric = false
					break
				}
			}
			if numeric && name != "" {
				name = "$" + name
			}
		}
	}
	return fmt.Sprintf("%s: %s", name, u.Message)
}

// BadSubstitutionError is returned for malformed parameter expansions which
// bash accepts at parse time but rejects during expansion.
type BadSubstitutionError struct {
	Node *syntax.ParamExp
}

func (b BadSubstitutionError) Error() string { return "bad substitution" }

// IndirectExpansionError is returned when an indirect expansion ${!var}
// fails because var itself is unset. Bash treats the failure as fatal
// unless a default-style operator (:- and friends) follows the
// indirection, in which case the command fails with $? = 1 and
// execution continues (errors6.sub lines 50-51).
type IndirectExpansionError struct {
	Name     string
	NonFatal bool
}

func (i IndirectExpansionError) Error() string {
	return i.Name + ": invalid indirect expansion"
}

func bashLengthSliceExpr(pe *syntax.ParamExp, expr syntax.ArithmExpr) syntax.ArithmExpr {
	if pe.Param == nil || pe.Param.Value != "#" || expr == nil {
		return expr
	}
	return &syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{ValuePos: pe.Param.Pos(), Value: "#: " + nodeLit(expr)},
	}}
}

func bashParamSliceExpr(pe *syntax.ParamExp, expr syntax.ArithmExpr) syntax.ArithmExpr {
	if pe.Param == nil || expr == nil {
		return expr
	}
	return &syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{ValuePos: pe.Param.Pos(), Value: pe.Param.Value + ": " + nodeLit(expr)},
	}}
}

func bashParamSliceText(pe *syntax.ParamExp, err error) string {
	if pe.Param == nil {
		return ""
	}
	msg := err.Error()
	const prefix = "error token is \""
	start := strings.Index(msg, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.IndexByte(msg[start:], '"')
	if end < 0 {
		return ""
	}
	return pe.Param.Value + ": " + msg[start:start+end]
}

func validIndirectName(name string) bool {
	switch name {
	case "#", "@", "*", "?", "-", "$", "!", "0":
		return true
	}
	if syntax.ValidName(name) {
		return true
	}
	if _, _, ok := splitIndirectArrayRef(name); ok {
		return true
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return name != ""
}

func splitIndirectArrayRef(ref string) (base string, index syntax.ArithmExpr, ok bool) {
	base, rest, ok := strings.Cut(ref, "[")
	if !ok || !strings.HasSuffix(rest, "]") || !syntax.ValidName(base) {
		return "", nil, false
	}
	idx := strings.TrimSuffix(rest, "]")
	if idx == "" {
		return "", nil, false
	}
	return base, nameRefArrayTargetIndex(idx), true
}

func cannotAssignParam(name string) bool {
	switch name {
	case "@", "*", "#", "?", "-", "$", "!", "0":
		return true
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return name != ""
}

func overridingUnset(pe *syntax.ParamExp) bool {
	if pe.Exp == nil {
		return false
	}
	switch pe.Exp.Op {
	case syntax.AlternateUnset, syntax.AlternateUnsetOrNull,
		syntax.DefaultUnset, syntax.DefaultUnsetOrNull,
		syntax.ErrorUnset, syntax.ErrorUnsetOrNull,
		syntax.AssignUnset, syntax.AssignUnsetOrNull:
		return true
	}
	return false
}

func bashAlternateCommandSubstEOF(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		sq, ok := part.(*syntax.SglQuoted)
		if !ok {
			continue
		}
		idx := strings.Index(sq.Value, "$(")
		if idx >= 0 && !strings.Contains(sq.Value[idx+2:], ")") {
			return true
		}
	}
	return false
}

// applyParamMods applies the trailing modifier portion of a parameter
// expansion (Slice, Repl, Exp) to a precomputed string value. Used
// by the indirect-expansion path (`${!x//c/x}`) where the value
// comes from a name lookup but the substitution should still apply.
func applyParamMods(cfg *Config, pe *syntax.ParamExp, str string) (string, error) {
	out, err := applyParamModsElems(cfg, pe, []string{str})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], nil
}

func applyParamModsElems(cfg *Config, pe *syntax.ParamExp, elems []string) ([]string, error) {
	if pe.Repl == nil && (pe.Exp == nil || !indirectParamModOp(pe.Exp.Op)) {
		return elems, nil
	}
	out := slices.Clone(elems)
	if pe.Exp != nil && indirectParamModOp(pe.Exp.Op) {
		arg, err := Pattern(cfg, pe.Exp.Word)
		if err != nil {
			return nil, err
		}
		suffix := pe.Exp.Op == syntax.RemSmallSuffix || pe.Exp.Op == syntax.RemLargeSuffix
		small := pe.Exp.Op == syntax.RemSmallPrefix || pe.Exp.Op == syntax.RemSmallSuffix
		for i, elem := range out {
			out[i] = cfg.removePattern(elem, arg, suffix, small)
		}
	}
	if pe.Repl == nil {
		return out, nil
	}
	orig, replAnchoredStart, replAnchoredEnd, err := replPattern(cfg, pe.Repl.Orig, pe.Repl.All)
	if err != nil {
		return nil, err
	}
	if orig == "" && !replAnchoredStart && !replAnchoredEnd {
		return out, nil
	}
	segs, err := cfg.replTemplate(pe.Repl.With)
	if err != nil {
		return nil, err
	}
	matchSpecial := replHasMatch(segs)
	with := renderRepl(segs, "")
	n := 1
	if pe.Repl.All {
		n = -1
	}
	for i, elem := range out {
		locs := cfg.findReplIndex(orig, elem, n, replAnchoredStart, replAnchoredEnd)
		var sb strings.Builder
		last := 0
		for _, loc := range locs {
			sb.WriteString(elem[last:loc[0]])
			if matchSpecial {
				sb.WriteString(renderRepl(segs, elem[loc[0]:loc[1]]))
			} else {
				sb.WriteString(with)
			}
			last = loc[1]
		}
		sb.WriteString(elem[last:])
		out[i] = sb.String()
	}
	return out, nil
}

func replPattern(cfg *Config, word *syntax.Word, all bool) (pat string, start, end bool, err error) {
	pat, err = Pattern(cfg, word)
	if err != nil || word == nil || len(word.Parts) == 0 {
		return pat, false, false, err
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok || lit.Value == "" {
		return pat, false, false, nil
	}
	if !all {
		switch lit.Value[0] {
		case '#':
			return strings.TrimPrefix(pat, "#"), true, false, nil
		case '%':
			return strings.TrimPrefix(pat, "%"), false, true, nil
		}
	}
	return pat, false, false, nil
}

func (cfg *Config) findReplIndex(pat, name string, n int, start, end bool) [][]int {
	if pat == "" {
		if start {
			return [][]int{{0, 0}}
		}
		if end {
			return [][]int{{len(name), len(name)}}
		}
		return nil
	}
	if cfg.cLocale() {
		return findReplIndexBytes(pat, name, n, start, end, cfg.NoCaseMatch)
	}
	if pat == "[^]]" {
		return nil
	}
	if strings.Contains(pat, "[") && strings.Contains(pat, "-") {
		return findReplIndexBytes(pat, name, n, start, end, cfg.NoCaseMatch)
	}
	if !start && !end {
		return cfg.findAllIndex(pat, name, n)
	}
	var mode pattern.Mode
	if cfg.ExtGlob {
		mode |= pattern.ExtendedOperators
	}
	if strings.Contains(pat, "-") {
		mode |= pattern.LenientRanges
	}
	if cfg.NoCaseMatch {
		mode |= pattern.NoGlobCase
	}
	// A `[` with no closing `]` is a literal `[` in bash (`${var#[}` on
	// `[foo]` -> `foo]`), same as the patsub path in findAllIndex. Without
	// this, pattern.Regexp rejects the unmatched `[` and the strip no-ops.
	expr, err := pattern.Regexp(escapeOrphanBrackets(escapeEmptyBracketClass(pat)), mode)
	if err != nil {
		return nil
	}
	switch {
	case start:
		expr = "^(" + expr + ")"
	case end:
		expr = "(" + expr + ")$"
	}
	rx, err := regexp.Compile(expr)
	if err != nil {
		return nil
	}
	if loc := rx.FindStringSubmatchIndex(name); loc != nil && len(loc) >= 4 {
		return [][]int{{loc[2], loc[3]}}
	}
	return nil
}

func findReplIndexBytes(pat, name string, n int, start, end, nocase bool) [][]int {
	if start {
		for j := len(name); j >= 0; j-- {
			if bytePatternMatch([]byte(pat), []byte(name[:j]), nocase) {
				return [][]int{{0, j}}
			}
		}
		return nil
	}
	if end {
		for i := 0; i <= len(name); i++ {
			if bytePatternMatch([]byte(pat), []byte(name[i:]), nocase) {
				return [][]int{{i, len(name)}}
			}
		}
		return nil
	}
	var locs [][]int
	for i := 0; i <= len(name); i++ {
		best := -1
		for j := len(name); j >= i; j-- {
			if bytePatternMatch([]byte(pat), []byte(name[i:j]), nocase) {
				best = j
				break
			}
		}
		if best < 0 {
			continue
		}
		locs = append(locs, []int{i, best})
		if n > 0 && len(locs) >= n {
			break
		}
		if best == i {
			continue
		}
		i = best - 1
	}
	return locs
}

func bytePatternMatch(pat, name []byte, nocase bool) bool {
	for len(pat) > 0 {
		switch p := pat[0]; p {
		case '*':
			for len(pat) > 1 && pat[1] == '*' {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			pat = pat[1:]
			for i := 0; i <= len(name); i++ {
				if bytePatternMatch(pat, name[i:], nocase) {
					return true
				}
			}
			return false
		case '?':
			if len(name) == 0 {
				return false
			}
			pat = pat[1:]
			name = name[1:]
		case '[':
			ok, consumed, matched := byteBracketMatch(pat, name, nocase)
			if !ok {
				return false
			}
			if !matched {
				return false
			}
			pat = pat[consumed:]
			name = name[1:]
		case '\\':
			if len(pat) == 1 {
				p = '\\'
				pat = pat[1:]
			} else {
				p = pat[1]
				pat = pat[2:]
			}
			if len(name) == 0 || !byteEqual(name[0], p, nocase) {
				return false
			}
			name = name[1:]
		default:
			if len(name) == 0 || !byteEqual(name[0], p, nocase) {
				return false
			}
			pat = pat[1:]
			name = name[1:]
		}
	}
	return len(name) == 0
}

func byteBracketMatch(pat, name []byte, nocase bool) (ok bool, consumed int, matched bool) {
	if len(name) == 0 || len(pat) < 3 || pat[0] != '[' {
		return false, 0, false
	}
	i := 1
	neg := false
	if pat[i] == '!' || pat[i] == '^' {
		neg = true
		i++
	}
	if i >= len(pat) {
		return false, 0, false
	}
	b := foldByte(name[0], nocase)
	in := false
	first := true
scan:
	for i < len(pat) {
		if pat[i] == ']' && !first {
			if neg {
				in = !in
			}
			return true, i + 1, in
		}
		start := pat[i]
		if start == '\\' && i+1 < len(pat) {
			i++
			start = pat[i]
			if start == '[' && i+2 < len(pat) && pat[i+1] == ']' && pat[i+2] == ']' {
				if byteEqual(b, '[', nocase) || byteEqual(b, ']', nocase) {
					in = true
				}
				if neg {
					in = !in
				}
				return true, i + 3, in
			}
		}
		if start == '[' && i+3 < len(pat) && pat[i+1] == ':' {
			for j := i + 2; j+1 < len(pat); j++ {
				if pat[j] == ':' && pat[j+1] == ']' {
					if byteClassMatch(string(pat[i+2:j]), b) {
						in = true
					}
					i = j + 2
					first = false
					continue scan
				}
			}
		}
		if i+2 < len(pat) && pat[i+1] == '-' && pat[i+2] != ']' {
			start = foldByte(start, nocase)
			end := foldByte(pat[i+2], nocase)
			if end == '\\' && i+3 < len(pat) {
				end = foldByte(pat[i+3], nocase)
				if start <= end && start <= b && b <= end {
					in = true
				}
				i += 4
			} else {
				if start <= end && start <= b && b <= end {
					in = true
				}
				i += 3
			}
			first = false
			continue
		}
		if byteEqual(b, start, nocase) {
			in = true
		}
		i++
		first = false
	}
	return false, 0, false
}

func foldByte(b byte, nocase bool) byte {
	if nocase && 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func byteEqual(a, b byte, nocase bool) bool {
	return foldByte(a, nocase) == foldByte(b, nocase)
}

func byteClassMatch(class string, b byte) bool {
	switch class {
	case "alnum":
		return '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	case "alpha":
		return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	case "digit":
		return '0' <= b && b <= '9'
	case "lower":
		return 'a' <= b && b <= 'z'
	case "upper":
		return 'A' <= b && b <= 'Z'
	case "xdigit":
		return '0' <= b && b <= '9' || 'a' <= b && b <= 'f' || 'A' <= b && b <= 'F'
	case "blank":
		return b == ' ' || b == '\t'
	case "space":
		return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
	case "punct":
		return 0x21 <= b && b <= 0x2f || 0x3a <= b && b <= 0x40 ||
			0x5b <= b && b <= 0x60 || 0x7b <= b && b <= 0x7e
	case "cntrl":
		return b < 0x20 || b == 0x7f
	case "graph":
		return 0x21 <= b && b <= 0x7e
	case "print":
		return 0x20 <= b && b <= 0x7e
	}
	return false
}

// replSegment is one piece of a ${var/pat/repl} replacement template. When
// match is true the matched portion of the value is substituted here;
// otherwise lit is emitted verbatim.
type replSegment struct {
	lit   string
	match bool
}

// replTemplate builds the replacement template for the With word of a
// substitution. Backslash escapes and quoting are resolved up front; when
// patsub_replacement is enabled an unquoted `&` becomes a match placeholder.
// Tilde expansion is applied to a leading unquoted `~`.
func (cfg *Config) replTemplate(with *syntax.Word) ([]replSegment, error) {
	var segs []replSegment
	if with == nil {
		return segs, nil
	}
	emitLit := func(s string) {
		if s == "" {
			return
		}
		if n := len(segs); n > 0 && !segs[n-1].match {
			segs[n-1].lit += s
			return
		}
		segs = append(segs, replSegment{lit: s})
	}
	patsub := cfg.PatSubReplacement
	// scanUnquoted processes source/expanded text that is subject to
	// patsub `&` substitution and backslash escaping.
	scanUnquoted := func(s string, fromSource bool) {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				next := s[i+1]
				// In bare source text a backslash escapes any
				// character. In expanded variable content only `\&`
				// and `\\` are special; other backslashes are kept.
				if fromSource || next == '&' || next == '\\' {
					emitLit(string(next))
					i++
					continue
				}
				emitLit("\\")
				continue
			}
			if c == '&' && patsub {
				segs = append(segs, replSegment{match: true})
				continue
			}
			emitLit(string(c))
		}
	}
	for idx, part := range with.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			val := part.Value
			if idx == 0 {
				if prefix, rest := cfg.expandUser(val, len(with.Parts) > 1); prefix != "" {
					emitLit(prefix)
					val = rest
				}
			}
			scanUnquoted(val, true)
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
			s, err := Literal(cfg, &syntax.Word{Parts: []syntax.WordPart{part}})
			if err != nil {
				return nil, err
			}
			scanUnquoted(s, false)
		default:
			// Quoted parts (SglQuoted, DblQuoted): fully literal, no
			// `&` substitution or backslash processing here.
			s, err := Literal(cfg, &syntax.Word{Parts: []syntax.WordPart{part}})
			if err != nil {
				return nil, err
			}
			emitLit(s)
		}
	}
	return segs, nil
}

// renderRepl renders a replacement template, substituting matched for each
// match placeholder.
func renderRepl(segs []replSegment, matched string) string {
	if len(segs) == 0 {
		return ""
	}
	if len(segs) == 1 && !segs[0].match {
		return segs[0].lit
	}
	var sb strings.Builder
	for _, s := range segs {
		if s.match {
			sb.WriteString(matched)
		} else {
			sb.WriteString(s.lit)
		}
	}
	return sb.String()
}

// replHasMatch reports whether any segment substitutes the matched text.
func replHasMatch(segs []replSegment) bool {
	for _, s := range segs {
		if s.match {
			return true
		}
	}
	return false
}

func (cfg *Config) indirectAtValue(name, op string) string {
	if !syntax.ValidName(name) {
		return ""
	}
	vr := cfg.Env.Get(name)
	switch op {
	case "a":
		return vr.Flags()
	case "A":
		if !vr.Declared() {
			return ""
		}
		return cfg.paramAtA(vr, vr, name, vr.String(), false)
	case "Q":
		if !vr.IsSet() {
			return ""
		}
		return bashQuoteParamQ(vr.String())
	case "P":
		if !vr.IsSet() {
			return ""
		}
		return cfg.expandPrompt(vr.String())
	default:
		return ""
	}
}

func (cfg *Config) paramExp(pe *syntax.ParamExp) (string, error) {
	oldParam := cfg.curParam
	cfg.curParam = pe
	defer func() { cfg.curParam = oldParam }()

	if pe.BadSubst != nil {
		return "", BadSubstitutionError{Node: pe}
	}

	name := pe.Param.Value
	index := pe.Index
	hadIndex := index != nil
	if name == "!" && pe.Exp != nil && pe.Exp.Op == syntax.RemSmallPrefix && pe.Exp.Word == nil {
		return cfg.Env.Get(cfg.Env.Get("#").String()).String(), nil
	}
	switch name {
	case "@", "*":
		index = &syntax.Word{Parts: []syntax.WordPart{
			&syntax.Lit{Value: name},
		}}
	}
	var vr Variable
	switch name {
	case "LINENO":
		// This is the only parameter expansion that the environment
		// interface cannot satisfy. When the interpreter has set
		// [Config.OverrideLineno] (e.g. while expanding a trap
		// body), use it instead of the actual parser position.
		var line uint64
		if cfg.OverrideLineno > 0 {
			line = uint64(cfg.OverrideLineno)
		} else {
			line = uint64(cfg.curParam.Pos().Line())
		}
		vr = Variable{Set: true, Kind: String, Str: strconv.FormatUint(line, 10)}
	default:
		vr = cfg.Env.Get(name)
	}
	orig := vr
	if vr.Kind == NameRef && index == nil {
		if base, idx, ok := nameRefArrayTarget(vr.Str); ok {
			name = base
			vr = cfg.Env.Get(base)
			index = nameRefArrayTargetIndex(idx)
		}
	}
	if n, v, circular := vr.ResolveTracked(cfg.Env); n != "" {
		name, vr = n, v
		if circular && cfg.OnNameRefCircular != nil {
			cfg.OnNameRefCircular(pe.Param.Value)
		}
	}
	// For a subscripted read (`${a[i]}`), nounset must test whether THAT
	// element is set, not whether the whole array is: vr.IsSet() reflects
	// only the array's scalar (element-0) value, so `set -u; a[2]=z; echo
	// ${a[2]}` wrongly tripped "unbound variable" even though a[2] is set.
	// Only evaluate the element under set -u — arrayElemSet evaluates the
	// subscript, and doing so unconditionally would double-evaluate a
	// side-effecting index like `${a[i++]}`.
	addressedSet := vr.IsSet()
	if cfg.NoUnset && index != nil && nodeLit(index) != "@" && nodeLit(index) != "*" {
		addressedSet = arrayElemSet(vr, index, cfg)
	}
	if cfg.NoUnset && !addressedSet && !overridingUnset(pe) &&
		!(orig.Kind == NameRef && index != nil && vr.Kind == Indexed) &&
		nodeLit(index) != "@" && nodeLit(index) != "*" {
		if index != nil {
			idxText := nodeLit(index)
			if vr.Kind != Associative {
				i, err := Arithm(cfg, index)
				if err != nil {
					return "", err
				}
				if idxText == "" {
					idxText = strconv.Itoa(i)
				}
			}
			if idxText == "" && vr.Kind == Indexed {
				if i, err := Arithm(cfg, index); err == nil {
					idxText = strconv.Itoa(i)
				}
			}
			if idxText != "" {
				errName := fmt.Sprintf("%s[%s]", name, idxText)
				if orig.Kind == NameRef && !hadIndex {
					errName = pe.Param.Value
				}
				return "", UnsetParameterError{
					Name:    errName,
					Message: "unbound variable",
				}
			}
		}
		return "", UnsetParameterError{
			Node:    pe,
			Message: "unbound variable",
		}
	}

	var sliceOffset, sliceLen int
	if pe.Slice != nil {
		var err error
		if pe.Slice.Offset != nil {
			if pe.Param != nil && pe.Param.Value == "#" && nodeLit(pe.Slice.Offset) == "%" {
				return "", &ArithmError{
					Expr: bashLengthSliceExpr(pe, pe.Slice.Offset),
					Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", "%"),
				}
			}
			sliceOffset, err = Arithm(cfg, pe.Slice.Offset)
			if err != nil {
				if arithErr, ok := err.(*ArithmError); ok {
					arithErr.Expr = bashLengthSliceExpr(pe, arithErr.Expr)
				} else {
					err = &ArithmError{
						Expr: bashParamSliceExpr(pe, pe.Slice.Offset),
						Text: bashParamSliceText(pe, err),
						Err:  err,
					}
				}
				return "", err
			}
		}
		if pe.Slice.Length != nil {
			sliceLen, err = Arithm(cfg, pe.Slice.Length)
			if err != nil {
				return "", err
			}
		}
	}

	var (
		str   string
		elems []string

		indexAllElements bool // true if var has been accessed with * or @ index
		callVarInd       = true
		assocElemSet     bool
	)

	switch nodeLit(index) {
	case "@", "*":
		allOp := nodeLit(index)
		switch vr.Kind {
		case Unknown:
			elems = nil
			indexAllElements = true
		case Indexed:
			indexAllElements = true
			callVarInd = false
			var err error
			elems, err = cfg.sliceIndexedElems(pe, vr, name == "@" || name == "*")
			if err != nil {
				return "", err
			}
			if allOp == "*" && name != "@" {
				// `$*` / `${a[*]}` join with the first IFS char, or with
				// nothing when IFS is empty. ifsJoin handles the empty-IFS
				// case correctly (concatenation); a hardcoded space here
				// would wrongly turn `${x#$*}` under IFS= into a space-
				// separated pattern.
				str = cfg.ifsJoin(elems)
			} else {
				str = strings.Join(elems, " ")
			}
		case Associative:
			indexAllElements = true
			callVarInd = false
			// Bash iterates assoc-array values in its hash-table
			// order, the same order `declare -p` prints (honoring the
			// variable's bucket count via AssocKeysForDeclare).
			keys := vr.AssocKeysForDeclare()
			elems = make([]string, len(keys))
			for i, k := range keys {
				elems[i] = vr.Map[k]
			}
			if pe.Slice != nil {
				var err error
				elems, err = cfg.sliceAssocElems(pe, elems)
				if err != nil {
					return "", err
				}
			}
			if allOp == "*" && name != "@" {
				// `$*` / `${a[*]}` join with the first IFS char, or with
				// nothing when IFS is empty. ifsJoin handles the empty-IFS
				// case correctly (concatenation); a hardcoded space here
				// would wrongly turn `${x#$*}` under IFS= into a space-
				// separated pattern.
				str = cfg.ifsJoin(elems)
			} else {
				str = strings.Join(elems, " ")
			}
		}
	}
	if callVarInd {
		if vr.Kind == Associative && index != nil {
			key, err := assocSubscriptKey(cfg, index)
			if err != nil {
				return "", err
			}
			if key == "" {
				return "", fmt.Errorf("[%s]: bad array subscript", subscriptText(index))
			}
			str, assocElemSet = vr.Map[key]
		} else {
			var err error
			str, err = cfg.varInd(vr, index)
			if err != nil {
				return "", err
			}
		}
	}
	if !indexAllElements {
		elems = []string{str}
	}
	joinModifiedElems := func(out []string) string {
		// A `[*]` index joins the (modified) elements with the first
		// character of IFS, for indexed and associative arrays alike;
		// `[@]` joins with a plain space when collapsed to a scalar.
		if indexAllElements && nodeLit(index) == "*" {
			return cfg.ifsJoin(out)
		}
		return strings.Join(out, " ")
	}

	switch {
	case pe.Names != 0 && !pe.Excl:
		// `${name*}` without `!` — bash 5.3 parses this but emits
		// `bad substitution` at expansion time.
		return "", BadSubstitutionError{Node: pe}
	case pe.Length:
		n := len(elems)
		switch nodeLit(index) {
		case "@", "*":
		default:
			n = cfg.paramStringLen(str)
		}
		str = strconv.Itoa(n)
	case pe.Excl:
		if pe.Exp != nil && !indirectDefaultOp(pe.Exp.Op) && !indirectAtOp(pe.Exp.Op) && !indirectParamModOp(pe.Exp.Op) {
			return "", BadSubstitutionError{Node: pe}
		}
		var strs []string
		indirectName := name
		indirectOrig := vr
		indirectIndex := index
		applyMod := false
		indirectAtApplied := false
		sortStrs := false
		_, _, refArrayElem := nameRefArrayTarget(orig.Str)
		switch {
		case pe.Names != 0:
			if !syntax.ValidName(pe.Param.Value) {
				return "", BadSubstitutionError{Node: pe}
			}
			strs = cfg.namesByPrefix(pe.Param.Value)
			sortStrs = true
		case orig.Kind == NameRef && pe.Index != nil &&
			(nodeLit(pe.Index) == "@" || nodeLit(pe.Index) == "*") &&
			refArrayElem:
			// A nameref to one array element has no array of indexes of
			// its own. Bash expands ${!ref[@]} to zero fields here.
			return "", nil
		case pe.Exp == nil && pe.Index != nil && vr.Kind == Indexed &&
			(nodeLit(pe.Index) == "@" || nodeLit(pe.Index) == "*"):
			if pe.Exp != nil && indirectAtOp(pe.Exp.Op) {
				lit := ""
				if pe.Exp.Word != nil && len(pe.Exp.Word.Parts) == 1 {
					if part, ok := pe.Exp.Word.Parts[0].(*syntax.Lit); ok {
						lit = part.Value
					}
				}
				keys := vr.IndexedIndexes()
				for _, i := range keys {
					key := strconv.Itoa(i)
					strs = append(strs, cfg.indirectAtValue(key, lit))
				}
				indirectAtApplied = true
				break
			}
			for _, i := range vr.IndexedIndexes() {
				strs = append(strs, strconv.Itoa(i))
			}
		case pe.Index != nil && vr.Kind == Associative:
			if pe.Exp != nil && indirectAtOp(pe.Exp.Op) {
				lit := ""
				if pe.Exp.Word != nil && len(pe.Exp.Word.Parts) == 1 {
					if part, ok := pe.Exp.Word.Parts[0].(*syntax.Lit); ok {
						lit = part.Value
					}
				}
				keys := vr.AssocKeysForDeclare()
				for _, key := range keys {
					strs = append(strs, cfg.indirectAtValue(key, lit))
				}
				indirectAtApplied = true
				break
			}
			strs = vr.AssocKeysForDeclare()
		case orig.Kind == NameRef && pe.Index != nil && nodeLit(pe.Index) != "@" && nodeLit(pe.Index) != "*":
			if vr.IsSet() {
				strs = append(strs, "")
			} else {
				// `${!ref[idx]}` on an unset nameref: bash prints the
				// diagnostic, sets $? = 1, and keeps running (it is not
				// the fatal bare-`${!var}` form).
				return "", IndirectExpansionError{Name: fmt.Sprintf("%s[%s]", pe.Param.Value, nodeLit(pe.Index)), NonFatal: true}
			}
		case orig.Kind == NameRef:
			strs = append(strs, orig.Str)
		case pe.Index != nil && (nodeLit(pe.Index) == "@" || nodeLit(pe.Index) == "*") && !vr.IsSet():
			return "", nil
		case (name == "@" || name == "*") && !vr.IsSet():
			return "", nil
		case !vr.IsSet():
			// Bash 5.3 includes the variable name in the message
			// (`./file: line N: foo: invalid indirect expansion`).
			if pe.Exp != nil && indirectDefaultOp(pe.Exp.Op) {
				if !syntax.ValidName(name) {
					// Unset positional/special parameter: the
					// default-style operator simply applies.
					break
				}
				return "", IndirectExpansionError{Name: name, NonFatal: true}
			}
			return "", IndirectExpansionError{Name: name}
		case str == "" && pe.Exp != nil && indirectDefaultOp(pe.Exp.Op):
			break
		case str == "":
			return "", nil
		default:
			if !validIndirectName(str) {
				return "", fmt.Errorf("%s: invalid variable name", str)
			}
			if base, idx, ok := splitIndirectArrayRef(str); ok {
				vr = cfg.Env.Get(base)
				indirectName = base
				indirectOrig = vr
				indirectIndex = idx
				switch vr.Kind {
				case Indexed:
					switch nodeLit(idx) {
					case "@", "*":
						strs = append(strs, vr.IndexedValues()...)
						indexAllElements = true
					default:
						val, err := cfg.varInd(vr, idx)
						if err != nil {
							return "", err
						}
						strs = append(strs, val)
					}
				case Associative:
					switch nodeLit(idx) {
					case "@", "*":
						keys := vr.AssocKeysForDeclare()
						for _, key := range keys {
							strs = append(strs, vr.Map[key])
						}
						indexAllElements = true
					default:
						val, err := cfg.varInd(vr, idx)
						if err != nil {
							return "", err
						}
						strs = append(strs, val)
					}
				default:
					val, err := cfg.varInd(vr, idx)
					if err != nil {
						return "", err
					}
					strs = append(strs, val)
				}
				applyMod = true
				break
			}
			indirectName = str
			vr = cfg.Env.Get(str)
			indirectOrig = vr
			switch str {
			case "@":
				strs = append(strs, vr.List...)
			case "*":
				strs = append(strs, cfg.ifsJoin(vr.List))
			default:
				strs = append(strs, vr.String())
			}
			// `${!x//c/x}` — apply trailing modifiers to the
			// dereferenced value (bash 5.3).
			applyMod = true
		}
		if sortStrs {
			slices.Sort(strs)
		}
		elems = strs
		// `${!name[*]}` joins the keys/indices with the first IFS char
		// (nothing when IFS is empty), like `${name[*]}`; the `[@]` form
		// joins with a space.
		if nodeLit(index) == "*" || nodeLit(indirectIndex) == "*" {
			str = cfg.ifsJoin(strs)
		} else {
			str = strings.Join(strs, " ")
		}
		if pe.Exp != nil && indirectDefaultOp(pe.Exp.Op) {
			arg, err := Literal(cfg, pe.Exp.Word)
			if err != nil {
				return "", err
			}
			setNonColon := paramIsSetNonColon(cfg, vr, indirectName, indirectIndex)
			indirectAllSet := indexAllElements && len(elems) > 0
			switch pe.Exp.Op {
			case syntax.AlternateUnset:
				if setNonColon || indirectAllSet {
					str = arg
				} else {
					str = ""
				}
			case syntax.AlternateUnsetOrNull:
				if indexAllElements && indirectAllSet || !indexAllElements && vr.IsSet() && str != "" {
					str = arg
				} else {
					str = ""
				}
			case syntax.DefaultUnset:
				if !setNonColon && !indirectAllSet {
					str = arg
				}
			case syntax.DefaultUnsetOrNull:
				if indexAllElements {
					if !indirectAllSet {
						str = arg
					}
				} else if !vr.IsSet() || str == "" {
					str = arg
				}
			case syntax.ErrorUnset:
				if !vr.IsSet() {
					if arg == "" {
						arg = "parameter not set"
					}
					return "", UnsetParameterError{Node: pe, Message: arg}
				}
			case syntax.ErrorUnsetOrNull:
				if !vr.IsSet() || str == "" {
					if arg == "" {
						arg = "parameter null or not set"
					}
					return "", UnsetParameterError{Node: pe, Message: arg}
				}
			}
		}
		if pe.Exp != nil && indirectAtOp(pe.Exp.Op) && !indirectAtApplied {
			if pe.Exp.Word == nil || len(pe.Exp.Word.Parts) != 1 {
				return "", BadSubstitutionError{Node: pe}
			}
			lit, ok := pe.Exp.Word.Parts[0].(*syntax.Lit)
			if !ok {
				return "", BadSubstitutionError{Node: pe}
			}
			switch lit.Value {
			case "Q":
				out := make([]string, len(elems))
				for i, elem := range elems {
					quoted, qerr := syntax.Quote(elem, syntax.LangBash)
					if qerr != nil {
						panic(qerr)
					}
					if quoted == elem {
						quoted = bashSingleQuote(elem)
					}
					out[i] = quoted
				}
				str = strings.Join(out, " ")
			case "a":
				if cfg.NoUnset && transformNounsetUnset(indirectOrig) {
					return "", UnsetParameterError{Name: "!" + name, Message: "unbound variable"}
				}
				str = indirectOrig.Flags()
			case "A":
				if cfg.NoUnset && transformNounsetUnset(indirectOrig) {
					return "", UnsetParameterError{Name: "!" + name, Message: "unbound variable"}
				}
				str = cfg.paramAtA(indirectOrig, indirectOrig, indirectName, str, indexAllElements)
			default:
				return "", BadSubstitutionError{Node: pe}
			}
		}
		if applyMod && (pe.Repl != nil || pe.Exp != nil && indirectParamModOp(pe.Exp.Op)) {
			if modElems, err := applyParamModsElems(cfg, pe, elems); err != nil {
				return "", err
			} else {
				elems = modElems
				str = strings.Join(elems, " ")
			}
		}
	case pe.Width:
		// mksh's ${%var}: character width of the string value.
		str = strconv.Itoa(cfg.paramStringLen(str))
	case pe.IsSet:
		// Zsh's ${+var}: 1 if set, 0 if unset.
		if vr.IsSet() {
			str = "1"
		} else {
			str = "0"
		}
	case pe.Slice != nil:
		if callVarInd {
			runes := []rune(str)
			start, end := 0, len(runes)
			slicePos := func(n int) int {
				if n < 0 {
					n = len(runes) + n
					if n < 0 {
						n = len(runes)
					}
				} else if n > len(runes) {
					n = len(runes)
				}
				return n
			}
			if pe.Slice.Offset != nil {
				start = slicePos(sliceOffset)
			}
			if pe.Slice.Length != nil {
				if sliceLen < 0 {
					end = slicePos(sliceLen)
				} else if start+sliceLen < end {
					end = start + sliceLen
				}
				if end < start {
					end = start
				}
			}
			str = string(runes[start:end])
		} // else, elems are already sliced
	case pe.Repl != nil:
		orig, replAnchoredStart, replAnchoredEnd, err := replPattern(cfg, pe.Repl.Orig, pe.Repl.All)
		if err != nil {
			return "", err
		}
		if orig == "" && !replAnchoredStart && !replAnchoredEnd {
			break // nothing to replace
		}
		// Bash 5.3 applies quote-removal to the replacement string:
		// a backslash in the *unquoted* portion escapes the next
		// character (`\'` → `'`, `\\` → `\`, `\&` → `&`). With the
		// patsub_replacement option an unquoted `&` is replaced by
		// the matched text. [Config.replTemplate] resolves all of
		// this into a template rendered per match.
		segs, terr := cfg.replTemplate(pe.Repl.With)
		if terr != nil {
			return "", terr
		}
		matchSpecial := replHasMatch(segs)
		with := renderRepl(segs, "")
		n := 1
		if pe.Repl.All {
			n = -1
		}
		replaceOne := func(elem string) string {
			locs := cfg.findReplIndex(orig, elem, n, replAnchoredStart, replAnchoredEnd)
			sb := cfg.strBuilder()
			last := 0
			for _, loc := range locs {
				sb.WriteString(elem[last:loc[0]])
				if matchSpecial {
					sb.WriteString(renderRepl(segs, elem[loc[0]:loc[1]]))
				} else {
					sb.WriteString(with)
				}
				last = loc[1]
			}
			sb.WriteString(elem[last:])
			return sb.String()
		}
		if indexAllElements && nodeLit(index) == "*" && vr.Kind != Associative && !replAnchoredStart && !replAnchoredEnd {
			target := str
			if name == "*" {
				target = cfg.ifsJoin(elems)
			}
			str = replaceOne(target)
			break
		}
		out := slices.Clone(elems)
		for i, elem := range out {
			out[i] = replaceOne(elem)
		}
		str = joinModifiedElems(out)
	case pe.Exp != nil:
		// Bash 5.3 keeps `$'…'` ANSI-C sequences literal inside the
		// substitute text of a default-value parameter expansion
		// (`${var-DEFAULT}`, `${var+ALT}`, `${var=ASSIGN}`,
		// `${var?ERR}`) when the whole thing is evaluated for a
		// heredoc body. Other operations (pattern strip, replace,
		// substring, case-fold) still decode `$'…'` normally.
		op := pe.Exp.Op
		isDefaultLike := op == syntax.AlternateUnset || op == syntax.AlternateUnsetOrNull ||
			op == syntax.DefaultUnset || op == syntax.DefaultUnsetOrNull ||
			op == syntax.AssignUnset || op == syntax.AssignUnsetOrNull ||
			op == syntax.ErrorUnset || op == syntax.ErrorUnsetOrNull
		isPatternOp := op == syntax.RemSmallPrefix || op == syntax.RemLargePrefix ||
			op == syntax.RemSmallSuffix || op == syntax.RemLargeSuffix
		// All-null aggregate, computed before the word so the word is only
		// expanded when actually used (see wordNeeded + the switch op).
		starAggregateNull := false
		if cfg.insideDoubleQuote && (name == "*" || index != nil && (nodeLit(index) == "@" || nodeLit(index) == "*")) {
			var values []string
			switch vr.Kind {
			case Indexed:
				values = vr.IndexedValues()
			case Associative:
				for _, key := range vr.AssocKeysForDeclare() {
					values = append(values, vr.Map[key])
				}
			}
			if name == "*" {
				starAggregateNull = len(values) == 1 && values[0] == ""
			} else {
				starAggregateNull = len(values) == 1 && values[0] == ""
			}
		}
		starEmptyIFSNonNull := !cfg.insideDoubleQuote && cfg.ifs == "" &&
			(name == "*" && len(vr.List) > 1 ||
				nodeLit(index) == "*" && vr.Kind == Indexed && vr.IndexedCount() > 1)
		// Expand the substitute word ONLY when it is used, so a command
		// substitution / arithmetic side effect in `${x:-$((i++))}` does not
		// run when x is set. These conditions mirror the `switch op` below.
		// An empty indexed/associative array has no element[0], so the
		// non-colon ${a-w}/${a+w} forms see it as unset (bash 5.3).
		// paramIsSetNonColon evaluates the array subscript (arrayElemSet ->
		// Arithm), so a side-effecting index like `${a[i++]#x}` would be
		// double-evaluated if computed unconditionally. Only the non-colon
		// `+`/`-` ops consume setNonColon, so compute it lazily for them alone.
		// For an associative element use the precomputed assocElemSet (bash
		// treats a missing assoc key as unset for ${a[k]+w}).
		setNonColon := false
		switch op {
		case syntax.AlternateUnset, syntax.DefaultUnset, syntax.AssignUnset:
			if vr.Kind == Associative && index != nil &&
				nodeLit(index) != "@" && nodeLit(index) != "*" {
				setNonColon = assocElemSet
			} else {
				setNonColon = paramIsSetNonColon(cfg, vr, name, index)
			}
		}
		wordNeeded := true
		switch op {
		case syntax.AlternateUnsetOrNull:
			wordNeeded = str != "" || starEmptyIFSNonNull
		case syntax.AlternateUnset:
			wordNeeded = setNonColon
		case syntax.DefaultUnset:
			wordNeeded = !setNonColon
		case syntax.DefaultUnsetOrNull:
			wordNeeded = str == "" && !starEmptyIFSNonNull || starAggregateNull
		case syntax.AssignUnset:
			wordNeeded = !setNonColon
		case syntax.AssignUnsetOrNull:
			wordNeeded = str == "" && !starEmptyIFSNonNull || starAggregateNull
		case syntax.ErrorUnset:
			wordNeeded = !vr.IsSet()
		case syntax.ErrorUnsetOrNull:
			wordNeeded = str == ""
		}
		var arg string
		var err error
		switch {
		case !wordNeeded:
			// Word unused (e.g. `${x:-WORD}` with x set) — skip expansion so
			// its command-sub / arithmetic side effects don't run.
		case cfg.inHeredocBody && isDefaultLike:
			arg, err = literalKeepAnsiC(cfg, pe.Exp.Word)
		case isPatternOp:
			// `${var%PAT}` family: quoted segments of PAT are
			// literal characters, not glob metacharacters.
			// `${P%"*"}` removes a literal `*`, not the longest
			// possible match.
			arg, err = Pattern(cfg, pe.Exp.Word)
		case op == syntax.AlternateUnset || op == syntax.AlternateUnsetOrNull:
			arg, err = cfg.literalParamExpWord(pe.Exp.Word, false)
		case op == syntax.AssignUnset || op == syntax.AssignUnsetOrNull:
			if cfg.ifs == "" && paramExpWordHasAtOrStar(pe.Exp.Word) {
				if assignVal, ok := cfg.simpleAtStarNullIFSAssign(pe.Exp.Word); ok {
					arg = assignVal
				} else if cfg.insideDoubleQuote {
					arg, err = cfg.literalParamExpWord(pe.Exp.Word, false)
				} else {
					arg, err = LiteralWithQuoteRemoval(cfg, pe.Exp.Word)
				}
			} else if cfg.insideDoubleQuote {
				arg, err = cfg.literalParamExpWord(pe.Exp.Word, false)
			} else {
				arg, err = LiteralWithQuoteRemoval(cfg, pe.Exp.Word)
			}
		case cfg.insideDoubleQuote && paramExpDefaultTriggers(op, vr, str) &&
			(op == syntax.DefaultUnset || op == syntax.DefaultUnsetOrNull) &&
			paramExpWordHasSingleQuotedLeadingDouble(pe.Exp.Word):
			arg, err = cfg.literalParamExpDoubleQuotedDefault(pe.Exp.Word)
		case cfg.insideDoubleQuote && paramExpDefaultTriggers(op, vr, str) &&
			(op == syntax.DefaultUnset || op == syntax.DefaultUnsetOrNull) &&
			(paramExpWordSingleQuotesOnly(pe.Exp.Word) || paramExpWordHasBackslashLit(pe.Exp.Word)):
			arg, err = cfg.literalParamExpWord(pe.Exp.Word, false)
		default:
			arg, err = Literal(cfg, pe.Exp.Word)
		}
		if err != nil {
			if (op == syntax.AlternateUnset || op == syntax.AlternateUnsetOrNull) &&
				vr.IsSet() && bashAlternateCommandSubstEOF(pe.Exp.Word) {
				return "", fmt.Errorf("command substitution: line %d: unexpected EOF while looking for matching `)'", pe.Pos().Line()+2)
			}
			return "", err
		}
		// An unquoted `${var=${*:off}}` (or array `[*]` slice) assigns a
		// star-form substring whose 0x7f bytes are quoted nulls; bash drops
		// them when storing into var, so `${var=${*:1}}` on a sole `$'\177'`
		// assigns the empty string.
		if (op == syntax.AssignUnset || op == syntax.AssignUnsetOrNull) &&
			!cfg.insideDoubleQuote && starSliceQuotedNull(pe.Exp.Word) {
			arg = stripQuotedNulls(arg)
		}
		switch op {
		case syntax.AlternateUnsetOrNull:
			// `${param:+word}`: a non-empty value here proves the
			// parameter is set and non-null, so the alternate applies
			// unconditionally. Don't gate on vr.IsSet(), which reflects
			// the whole array rather than the addressed element — an
			// element-assigned array (`A[k]=v`) leaves the variable's
			// Set flag false even though the element exists.
			if str == "" && !starEmptyIFSNonNull {
				break
			}
			if bashAlternateCommandSubstEOF(pe.Exp.Word) {
				return "", fmt.Errorf("command substitution: line %d: unexpected EOF while looking for matching `)'", pe.Pos().Line()+2)
			}
			str = arg
		case syntax.AlternateUnset:
			if setNonColon {
				if bashAlternateCommandSubstEOF(pe.Exp.Word) {
					return "", fmt.Errorf("command substitution: line %d: unexpected EOF while looking for matching `)'", pe.Pos().Line()+2)
				}
				str = arg
			}
		case syntax.DefaultUnset:
			if setNonColon {
				break
			}
			fallthrough
		case syntax.DefaultUnsetOrNull:
			if str == "" && !starEmptyIFSNonNull || starAggregateNull {
				str = arg
			}
		case syntax.ErrorUnset:
			if vr.IsSet() {
				break
			}
			if arg == "" {
				arg = "parameter not set"
			}
			fallthrough
		case syntax.ErrorUnsetOrNull:
			if str == "" {
				if arg == "" {
					arg = "parameter null or not set"
				}
				return "", UnsetParameterError{
					Node:    pe,
					Message: arg,
				}
			}
		case syntax.AssignUnset:
			if arrayElemSet(vr, index, cfg) {
				break
			}
			fallthrough
		case syntax.AssignUnsetOrNull:
			if str == "" {
				if cannotAssignParam(name) {
					return "", fmt.Errorf("$%s: cannot assign in this way", name)
				}
				if err := cfg.envSetIndex(name, index, arg); err != nil {
					return "", err
				}
				str = arg
				// Reflect the value-transforming attributes that the
				// assignment applies to the stored element, so the
				// expansion result matches: integer (`${a[42]=4+3}` → 7)
				// and case-folding (`${A[k]=foo}` on `declare -u` → FOO).
				if vr.Integer {
					if v, err := cfg.integerValue(arg); err == nil {
						str = v
					}
				} else {
					str = foldCaseAttr(vr, arg)
				}
			}
		case syntax.RemSmallPrefix, syntax.RemLargePrefix,
			syntax.RemSmallSuffix, syntax.RemLargeSuffix:
			suffix := op == syntax.RemSmallSuffix || op == syntax.RemLargeSuffix
			small := op == syntax.RemSmallPrefix || op == syntax.RemSmallSuffix
			out := slices.Clone(elems)
			for i, elem := range out {
				out[i] = cfg.removePattern(elem, arg, suffix, small)
			}
			str = joinModifiedElems(out)
		case syntax.UpperFirst, syntax.UpperAll,
			syntax.LowerFirst, syntax.LowerAll,
			syntax.CaseToggleFirst, syntax.CaseToggleAll:

			caseFunc := unicode.ToLower
			if op == syntax.UpperFirst || op == syntax.UpperAll {
				caseFunc = bashToUpper
			} else if op == syntax.CaseToggleFirst || op == syntax.CaseToggleAll {
				caseFunc = func(r rune) rune {
					if unicode.IsUpper(r) {
						return unicode.ToLower(r)
					}
					return bashToUpper(r)
				}
			}
			all := op == syntax.UpperAll || op == syntax.LowerAll || op == syntax.CaseToggleAll

			// empty string means '?'; nothing to do there
			expr, err := pattern.Regexp(arg, 0)
			if err != nil {
				return str, nil
			}
			rx, err := regexp.Compile(expr)
			if err != nil {
				return str, nil
			}

			// Casemod operates on a copy — `elems` may alias the
			// underlying array's List slice, and bash does not
			// mutate the variable when an expansion-time casemod
			// is applied.
			out := make([]string, len(elems))
			for i, elem := range elems {
				rs := []rune(elem)
				if all {
					for ri, r := range rs {
						if rx.MatchString(string(r)) {
							rs[ri] = caseFunc(r)
						}
					}
				} else if len(rs) > 0 && rx.MatchString(string(rs[0])) {
					// Single ^ / , / ~: only the first char of
					// each element is tested; no fall-through
					// to subsequent runes.
					rs[0] = caseFunc(rs[0])
				}
				out[i] = string(rs)
			}
			str = joinModifiedElems(out)
		case syntax.OtherParamOps:
			if pe.Exp.Word == nil || len(pe.Exp.Word.Parts) != 1 {
				return "", BadSubstitutionError{Node: pe}
			}
			if _, ok := pe.Exp.Word.Parts[0].(*syntax.Lit); !ok {
				return "", BadSubstitutionError{Node: pe}
			}
			switch arg {
			case "Q":
				// Bash 5.3's @Q on an unset variable (or unset array
				// element) expands to nothing, rather than quoting an
				// empty value to `''`. `@`/`*` and `[@]`/`[*]` forms are
				// "set" whenever any element exists, so they use IsSet
				// directly; a specific subscript checks that element.
				qSet := vr.IsSet()
				if name != "@" && name != "*" {
					if il := nodeLit(index); il != "@" && il != "*" {
						if vr.Kind == Associative {
							qSet = str != ""
							if !qSet {
								qSet = assocElemSet
							}
						} else if str != "" {
							// A non-empty value already proves the element is
							// set; only fall back to arrayElemSet (which
							// re-evaluates the subscript, re-running any command
							// substitution in it) when the value is empty.
							qSet = true
						} else {
							qSet = arrayElemSet(vr, index, cfg)
						}
					} else if len(elems) > 0 {
						// `${a[@]@Q}` / `${a[*]@Q}`: an element-assigned
						// array (`a[0]=''`) leaves vr.Set false even though
						// elements exist, so IsSet alone wrongly reports the
						// array unset. Any present element makes it "set".
						qSet = true
					}
				}
				if !qSet {
					str = ""
					break
				}
				// Bash 5.3 quotes each element of an array transform
				// separately (`${arr[@]@Q}` -> `'a' 'b'`).
				out := make([]string, len(elems))
				for i, elem := range elems {
					out[i] = bashQuoteParamQ(elem)
				}
				// The `*`/`[*]` scalar form joins the quoted elements
				// with the first IFS char (nothing when IFS is empty),
				// like `"${*@Q}"`; `@`/`[@]` joins with a space.
				if name == "*" || nodeLit(index) == "*" {
					str = cfg.ifsJoin(out)
				} else {
					str = strings.Join(out, " ")
				}
			case "E":
				tail := str
				var rns []rune
				for tail != "" {
					var rn rune
					rn, _, tail, _ = strconv.UnquoteChar(tail, 0)
					rns = append(rns, rn)
				}
				str = string(rns)
			case "a":
				if (name == "@" || name == "*") && indexAllElements {
					str = ""
					break
				}
				if cfg.NoUnset && transformNounsetUnset(orig) {
					return "", UnsetParameterError{Name: name, Message: "unbound variable"}
				}
				// ${var@a} returns variable attribute flags.
				// We use orig (before nameref resolve) for the attributes.
				str = orig.Flags()
			case "A":
				if cfg.NoUnset && transformNounsetUnset(orig) {
					return "", UnsetParameterError{Name: name, Message: "unbound variable"}
				}
				// ${var@A} returns a declare statement that recreates the
				// variable. A variable that was never declared expands to
				// nothing (a declared-but-unset variable still prints its
				// `declare` line).
				if !vr.Declared() && name != "@" && name != "*" {
					str = ""
					break
				}
				// ${var@A} returns a declare statement that recreates the variable.
				if name == "@" || name == "*" {
					// Positional parameters reproduce as `set -- ...`.
					out := make([]string, len(elems))
					for i, elem := range elems {
						out[i] = bashSingleQuote(elem)
					}
					str = "set -- " + strings.Join(out, " ")
				} else {
					str = cfg.paramAtA(vr, orig, name, str, indexAllElements)
				}
			case "U":
				str = bashToUpperString(str)
			case "u":
				if str != "" {
					r, size := utf8.DecodeRuneInString(str)
					str = string(bashToUpper(r)) + str[size:]
				}
			case "L":
				str = strings.ToLower(str)
			case "K":
				if !vr.IsSet() {
					str = ""
					break
				}
				str = cfg.paramAtK(vr, name)
			case "k":
				if !vr.IsSet() {
					str = ""
					break
				}
				if indexAllElements && nodeLit(index) == "*" {
					str = strings.Join(cfg.paramAtKFields(vr, name), " ")
				} else {
					str = cfg.paramAtK(vr, name)
				}
			case "P":
				if vr.Kind == Associative && !indexAllElements {
					str = ""
					break
				}
				str = cfg.expandPrompt(str)
			default:
				return "", BadSubstitutionError{Node: pe}
			}
		}
	}
	return str, nil
}

// paramAtK implements ${var@K} and ${var@k}, producing quoted key-value pairs
// for arrays. For indexed arrays: "0 val0 1 val1 ...".
// For associative arrays: "key1 val1 key2 val2 ...".
// For plain strings, returns the value unchanged.
// paramAtK implements the string form of ${var@K} and ${var@k}. A scalar
// becomes a single-quoted value (`'string'`); positional parameters become
// the single-quoted values without keys (`'a b' 'c d'`); indexed and
// associative arrays become a sequence of `key "value"` pairs with the
// value in double quotes, in bash key order.
func (cfg *Config) paramAtK(vr Variable, name string) string {
	if name == "@" || name == "*" {
		// Positional parameters: quoted values, no keys.
		var parts []string
		for _, v := range vr.List {
			parts = append(parts, bashQuoteParamQ(v))
		}
		return strings.Join(parts, " ")
	}
	switch vr.Kind {
	case Indexed:
		var parts []string
		for _, i := range vr.IndexedIndexes() {
			parts = append(parts, strconv.Itoa(i)+" "+bashDeclareQuote(vr.IndexedElem(i)))
		}
		return strings.Join(parts, " ")
	case Associative:
		var parts []string
		for _, k := range vr.AssocKeysForDeclare() {
			parts = append(parts, bashParamKKeyQuote(k)+" "+bashDeclareQuote(vr.Map[k]))
		}
		return strings.Join(parts, " ")
	default:
		return bashQuoteParamQ(vr.String())
	}
}

func bashParamKKeyQuote(s string) string {
	if s == "" {
		return bashDeclareQuote(s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z',
			'A' <= c && c <= 'Z',
			'0' <= c && c <= '9',
			c == '_', c == '.', c == '%', c == '-', c == '=':
			continue
		default:
			return bashDeclareQuote(s)
		}
	}
	return s
}

// paramAtKFields implements the field-splitting form of "${arr[@]@k}":
// each key and each (unquoted) value becomes a separate field.
func (cfg *Config) paramAtKFields(vr Variable, name string) []string {
	if name == "@" || name == "*" {
		return append([]string(nil), vr.List...)
	}
	switch vr.Kind {
	case Indexed:
		var out []string
		for _, i := range vr.IndexedIndexes() {
			out = append(out, strconv.Itoa(i), vr.IndexedElem(i))
		}
		return out
	case Associative:
		var out []string
		for _, k := range vr.AssocKeysForDeclare() {
			out = append(out, k, vr.Map[k])
		}
		return out
	default:
		return []string{vr.String()}
	}
}

// paramAtA implements ${var@A}: a reusable declaration that recreates the
// variable. For arrays bash emits the full `declare -a NAME=([i]="v" ...)` /
// `declare -A NAME=([k]="v" ...)` form (double-quoted element values, in
// bash's hash-bucket key order); for scalars it emits the single-quoted
// `[declare -flags ]NAME='value'` form. `scalarStr` is the already-expanded
// scalar value, and `orig` carries the scalar's attribute flags.
func (cfg *Config) paramAtA(vr, orig Variable, name, scalarStr string, forceLiteral bool) string {
	switch vr.Kind {
	case Indexed, Associative:
		if !forceLiteral {
			// A bare array name addresses element zero. Bash's @A
			// transformation therefore describes that scalar slot; [@] is
			// required to serialize the complete array.
			flags := vr.Flags()
			if flags == "" {
				flags = "-"
			}
			decl := "declare -" + flags + " " + name
			if vr.Kind == Indexed {
				if vr.IndexedSet(0) {
					return decl + "=" + bashSingleQuote(vr.IndexedElem(0))
				}
			} else if value, ok := vr.Map["0"]; ok {
				return decl + "=" + bashSingleQuote(value)
			}
			return decl
		}
		return declareArray(name, vr, forceLiteral)
	default:
		flags := orig.Flags()
		if !orig.IsSet() {
			// Declared but unset: bash prints just the attributes.
			if flags == "" {
				return name + "="
			}
			return "declare -" + flags + " " + name
		}
		if flags == "" {
			// A plain scalar (no attributes) is reproduced as `name=value`
			// with the value single-quoted, falling back to ANSI-C `$'…'`
			// quoting when it holds bytes single quotes can't represent
			// (control characters, DEL), exactly like ${var@Q}.
			return name + "=" + bashQuoteParamQ(scalarStr)
		}
		return "declare -" + flags + " " + name + "=" + bashSingleQuote(scalarStr)
	}
}

// declareArray formats an indexed or associative array as bash's ${arr@A}
// (and `declare -p`) reusable assignment.
//
// forceLiteral is true when the expansion used an explicit `[@]` / `[*]`
// subscript (`${arr[@]@A}`): bash then always emits the array literal for a
// *set* array, so an empty one yields `...NAME=()`. Without the subscript
// (`${arr@A}`) an array with no elements is reproduced as just the
// declaration (`declare -flags NAME`), matching bash treating an empty array
// as having no value. An unset-but-declared array never gets a value.
func declareArray(name string, vr Variable, forceLiteral bool) string {
	flags := vr.Flags()
	if flags == "" {
		flags = "-"
	}
	var b strings.Builder
	b.WriteString("declare -")
	b.WriteString(flags)
	b.WriteByte(' ')
	b.WriteString(name)
	switch vr.Kind {
	case Indexed:
		if forceLiteral {
			if !vr.IsSet() {
				return b.String()
			}
		} else if len(vr.IndexedIndexes()) == 0 {
			return b.String()
		}
		b.WriteString("=(")
		first := true
		for _, i := range vr.IndexedIndexes() {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			fmt.Fprintf(&b, "[%d]=%s", i, bashDeclareQuote(vr.IndexedElem(i)))
		}
		b.WriteByte(')')
	case Associative:
		if forceLiteral {
			if !vr.IsSet() {
				return b.String()
			}
		} else if len(vr.Map) == 0 {
			return b.String()
		}
		b.WriteString("=(")
		first := true
		for _, k := range vr.AssocKeysForDeclare() {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			fmt.Fprintf(&b, "[%s]=%s", bashAssocKeyQuote(k), bashDeclareQuote(vr.Map[k]))
		}
		// Bash leaves a trailing space before `)` for associative arrays.
		if !first {
			b.WriteByte(' ')
		}
		b.WriteByte(')')
	}
	return b.String()
}

// bashDeclareQuote formats v the way bash's declare -p / ${arr@A} array
// literals do: double-quoted, escaping ", \, $ and `, falling back to ANSI-C
// $'...' for non-printable bytes. (Mirrors the interp helper of the same name;
// kept here so the expand layer needn't import interp.)
func bashDeclareQuote(v string) string {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 || c == 0x7f {
			if q, err := syntax.Quote(v, syntax.LangBash); err == nil {
				return q
			}
			break
		}
	}
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '"' || c == '\\' || c == '$' || c == '`' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}

// bashAssocKeyQuote quotes an associative-array key for declare -p / ${arr@A}:
// bare when it contains only "safe" characters, double-quoted otherwise.
func bashAssocKeyQuote(s string) string {
	if s == "" {
		return bashDeclareQuote(s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z',
			'A' <= c && c <= 'Z',
			'0' <= c && c <= '9',
			c == '_', c == '.', c == '%', c == '-', c == '/', c == '=':
			continue
		default:
			return bashDeclareQuote(s)
		}
	}
	return s
}

// expandPrompt implements ${var@P} by delegating to [Config.PromptExpand].
// If PromptExpand is not set, it performs basic prompt escape expansion.
func (cfg *Config) expandPrompt(s string) string {
	return cfg.defaultPromptExpand(s)
}

// defaultPromptExpand handles a subset of Bash prompt escape sequences.
func (cfg *Config) defaultPromptExpand(s string) string {
	s = cfg.expandPromptVars(s)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '!' {
			if cfg.Posix {
				b.WriteByte('1')
			} else {
				b.WriteByte('!')
			}
			continue
		}
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			b.WriteByte('\\')
			break
		}
		switch s[i] {
		case 'a':
			b.WriteByte('\a')
		case 'h', 'H':
			host := cfg.envGet("HOSTNAME")
			if host == "" {
				host = cfg.envGet("HOST")
			}
			if s[i] == 'h' {
				if dot := strings.IndexByte(host, '.'); dot >= 0 {
					host = host[:dot]
				}
			}
			b.WriteString(host)
		case 'j':
			b.WriteByte('0')
		case 'e':
			b.WriteByte('\x1b')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 's':
			b.WriteString("bash")
		case 'v':
			b.WriteString("5.3")
		case 'V':
			b.WriteString("5.3.0")
		case 'w':
			b.WriteString(cfg.promptWorkingDir(false))
		case 'W':
			b.WriteString(cfg.promptWorkingDir(true))
		case '!':
			b.WriteByte('1')
		case '#':
			b.WriteByte('0')
		case '$':
			b.WriteByte('$')
		case '\\':
			b.WriteByte('\\')
		case '[':
			end := promptMarkerEnd(s, i+1)
			if end < 0 {
				break
			}
			inner := cfg.defaultPromptExpand(s[i+1 : end])
			switch inner {
			case "":
			case "\001":
				b.WriteByte('\001')
			default:
				b.WriteByte('\001')
				b.WriteString(inner)
				b.WriteByte('\002')
			}
			i = end + 1
		case ']':
		case '0', '1', '2', '3', '4', '5', '6', '7':
			end := i + 1
			for end < len(s) && end < i+3 && s[end] >= '0' && s[end] <= '7' {
				end++
			}
			val, _ := strconv.ParseUint(s[i:end], 8, 8)
			b.WriteByte(byte(val))
			i = end - 1
		default:
			// For unrecognized sequences, preserve them.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func promptMarkerEnd(s string, start int) int {
	for i := start; i+1 < len(s); i++ {
		if s[i] == '\\' && s[i+1] == ']' {
			return i
		}
	}
	return -1
}

func (cfg *Config) expandPromptVars(s string) string {
	if !strings.ContainsAny(s, "$`") {
		return s
	}
	word, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Document(strings.NewReader(s))
	if err != nil && err != io.EOF {
		return s
	}
	out, err := Literal(cfg, word)
	if err != nil {
		return s
	}
	return out
}

func (cfg *Config) promptWorkingDir(base bool) string {
	pwd := cfg.envGet("PWD")
	if pwd == "" {
		pwd = "."
	}
	home := cfg.envGet("HOME")
	if home != "" && (pwd == home || strings.HasPrefix(pwd, home+"/")) {
		pwd = "~" + strings.TrimPrefix(pwd, home)
	}
	if !base {
		return pwd
	}
	switch pwd {
	case "", "/":
		return "/"
	case "~":
		return "~"
	}
	return filepath.Base(pwd)
}

// escapeEmptyBracketClass escapes a `[` that opens a bracket expression bash
// cannot terminate because its only `]` is the literal first member: `[]`,
// `[!]`, `[^]`. escapeOrphanBrackets skips these because it sees a `]` and
// assumes the class closes; bash instead treats the `[` as a literal, so
// `${v#[]}` on `[]foo[]` yields `foo[]`. Applied before escapeOrphanBrackets,
// which then handles the remaining single-`[` orphan cases.
func escapeEmptyBracketClass(pat string) string {
	if !strings.Contains(pat, "[") {
		return pat
	}
	var b strings.Builder
	b.Grow(len(pat) + 2)
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		if c == '\\' && i+1 < len(pat) {
			b.WriteByte(c)
			b.WriteByte(pat[i+1])
			i++
			continue
		}
		if c == '[' {
			rest := pat[i+1:]
			if len(rest) > 0 && (rest[0] == '!' || rest[0] == '^') {
				rest = rest[1:]
			}
			// A leading `]` is a literal member; the class still needs a
			// further `]` to close. With none (and no `[` that could begin a
			// nested `[:class:]` element) the `[` is an unterminated literal.
			if len(rest) > 0 && rest[0] == ']' {
				after := rest[1:]
				if strings.IndexByte(after, ']') < 0 && strings.IndexByte(after, '[') < 0 {
					b.WriteString(`\[`)
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (cfg *Config) removePattern(str, pat string, fromEnd, shortest bool) string {
	mode := pattern.EntireString
	if cfg.ExtGlob {
		mode |= pattern.ExtendedOperators
	}
	localeMode := cfg.localePatternMode()
	if localeMode {
		mode |= pattern.Locale
	}
	// In a declared non-C, single-byte locale (e.g. de_DE.ISO-8859-1),
	// every byte is one character whose codepoint equals its byte
	// value: widen an invalid-UTF-8 pattern into runes so it flows
	// through the regexp-based matcher (bracket expressions, POSIX
	// classes, ranges) below rather than falling back to the
	// byte-blind matcher, which cannot see "[...]" at all.
	widen := localeMode && internal.SingleByteLocale(cfg.localeCategory("LC_CTYPE"))
	if widen {
		pat = internal.WidenLatin1(pat)
	}
	match := func(s string) bool {
		return false
	}
	splitPoints := removePatternSplitPoints
	if !utf8.ValidString(pat) {
		match = func(s string) bool {
			return internal.BytePatternMatch([]byte(pat), []byte(s))
		}
		splitPoints = removePatternByteSplitPoints
	} else {
		// A `[` with no closing `]` is a literal `[` in bash
		// (`${var#[}` on `[foo]` -> `foo]`); without this the matcher
		// rejects the unmatched `[` and the strip silently no-ops.
		matcher, err := internal.ExtendedPatternMatcher(escapeOrphanBrackets(escapeEmptyBracketClass(pat)), mode)
		if err != nil {
			return str
		}
		match = func(s string) bool {
			if widen {
				// Widen the candidate substring too, so raw
				// single-byte text compares against the widened
				// pattern as real characters instead of RuneError
				// stand-ins.
				s = internal.WidenLatin1(s)
			}
			return matcher(s)
		}
	}
	if !fromEnd {
		if shortest {
			for _, i := range splitPoints(str) {
				if match(str[:i]) {
					return str[i:]
				}
			}
		} else {
			for _, i := range slices.Backward(splitPoints(str)) {
				if match(str[:i]) {
					return str[i:]
				}
			}
		}
		return str
	}

	if shortest {
		for _, i := range slices.Backward(splitPoints(str)) {
			if match(str[i:]) {
				return str[:i]
			}
		}
	} else {
		for _, i := range splitPoints(str) {
			if match(str[i:]) {
				return str[:i]
			}
		}
	}
	return str
}

func removePatternSplitPoints(s string) []int {
	points := make([]int, 0, utf8.RuneCountInString(s)+1)
	points = append(points, 0)
	for i := range s {
		if i > 0 {
			points = append(points, i)
		}
	}
	points = append(points, len(s))
	return points
}

func removePatternByteSplitPoints(s string) []int {
	points := make([]int, len(s)+1)
	for i := range points {
		points[i] = i
	}
	return points
}

func (cfg *Config) varInd(vr Variable, idx syntax.ArithmExpr) (string, error) {
	if idx == nil {
		if vr.Kind == Associative {
			return vr.Map["0"], nil
		}
		return vr.String(), nil
	}
	switch vr.Kind {
	case String:
		switch nodeLit(idx) {
		case "*", "@":
			return vr.Str, nil
		}
		n, err := Arithm(cfg, idx)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return vr.Str, nil
		}
	case Indexed:
		switch nodeLit(idx) {
		case "*", "@":
			return strings.Join(vr.IndexedValues(), " "), nil
		}
		if emptyLiteralIndex(idx) && cfg.curParam.Param != nil {
			return "", BadSubstitutionError{Node: cfg.curParam}
		}
		if text, ok := singleQuotedIndex(idx); ok {
			return "", &ArithmError{
				Text: "'" + text + "'",
				Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", "'"+text+"'"),
			}
		}
		if start, end, ok := indexedBraceRange(idx); ok {
			var vals []string
			step := 1
			if start > end {
				step = -1
			}
			for i := start; ; i += step {
				if vr.IndexedSet(i) {
					vals = append(vals, vr.IndexedElem(i))
				}
				if i == end {
					break
				}
			}
			return strings.Join(vals, " "), nil
		}
		i, err := Arithm(cfg, indexedQuotedLiteralIndex(idx))
		if err != nil {
			return "", err
		}
		if i < 0 {
			orig := i
			i = indexedNegativeOffset(vr, i)
			if i < 0 && cfg.curParam.Length {
				idxText := subscriptText(idx)
				if idxText == "" {
					idxText = strconv.Itoa(orig)
				}
				return "", fmt.Errorf("[%s]: bad array subscript", idxText)
			}
			if i < 0 {
				name := ""
				if cfg.curParam.Param != nil {
					name = cfg.curParam.Param.Value
				}
				return "", fmt.Errorf("%s: bad array subscript", name)
			}
		}
		if vr.IndexedSet(i) {
			return vr.IndexedElem(i), nil
		}
		// A default/alternate operator deliberately handles an absent element,
		// so nounset must not preempt it.  curParam can also be nil during the
		// quoted-substitution trigger probe; that probe must observe the missing
		// value rather than dereference a nonexistent expansion context.
		if cfg.NoUnset && cfg.curParam != nil && !overridingUnset(cfg.curParam) {
			idxText := nodeLit(idx)
			if idxText == "" {
				idxText = strconv.Itoa(i)
			}
			errName := ""
			if cfg.curParam.Param != nil {
				errName = cfg.curParam.Param.Value
			}
			if cfg.curParam.Index != nil && errName != "" {
				errName = fmt.Sprintf("%s[%s]", errName, idxText)
			}
			return "", UnsetParameterError{
				Name:    errName,
				Message: "unbound variable",
			}
		}
	case Associative:
		switch lit := nodeLit(idx); lit {
		case "@", "*":
			keys := vr.AssocKeysForDeclare()
			strs := make([]string, len(keys))
			for i, k := range keys {
				strs[i] = vr.Map[k]
			}
			if lit == "*" {
				return cfg.ifsJoin(strs), nil
			}
			return strings.Join(strs, " "), nil
		}
		// A subscript key undergoes quote removal: an unquoted `\]` (or
		// `\[`) in `${a[\]]}` resolves to the literal key `]`, matching the
		// way the assignment side stored it.
		val, err := assocSubscriptKey(cfg, idx)
		if err != nil {
			return "", err
		}
		if val == "" {
			return "", fmt.Errorf("[%s]: bad array subscript", subscriptText(idx))
		}
		return vr.Map[val], nil
	}
	return "", nil
}

func assocSubscriptKey(cfg *Config, idx syntax.ArithmExpr) (string, error) {
	if word, ok := idx.(*syntax.Word); ok {
		return LiteralWithQuoteRemoval(cfg, word)
	}
	return compactArithmText(idx), nil
}

func emptyLiteralIndex(idx syntax.ArithmExpr) bool {
	word, ok := idx.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	return ok && lit.Value == ""
}

func singleQuotedIndex(idx syntax.ArithmExpr) (string, bool) {
	word, ok := idx.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return "", false
	}
	sq, ok := word.Parts[0].(*syntax.SglQuoted)
	if !ok {
		return "", false
	}
	return sq.Value, true
}

func indexedBraceRange(idx syntax.ArithmExpr) (int, int, bool) {
	word, ok := idx.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return 0, 0, false
	}
	dq, ok := word.Parts[0].(*syntax.DblQuoted)
	if !ok || len(dq.Parts) != 1 {
		return 0, 0, false
	}
	lit, ok := dq.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(lit.Value, "{") || !strings.HasSuffix(lit.Value, "}") {
		return 0, 0, false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(lit.Value, "{"), "}")
	parts := strings.Split(inner, "..")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return start, end, true
}

func indexedQuotedLiteralIndex(idx syntax.ArithmExpr) syntax.ArithmExpr {
	word, ok := idx.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return idx
	}
	dq, ok := word.Parts[0].(*syntax.DblQuoted)
	if !ok || len(dq.Parts) != 1 {
		return idx
	}
	lit, ok := dq.Parts[0].(*syntax.Lit)
	if !ok {
		return idx
	}
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: strings.TrimSpace(lit.Value)}}}
}

func subscriptText(idx syntax.ArithmExpr) string {
	if lit := nodeLit(idx); lit != "" {
		return lit
	}
	var b strings.Builder
	syntax.NewPrinter().Print(&b, idx)
	return b.String()
}

func compactArithmText(expr syntax.ArithmExpr) string {
	switch x := expr.(type) {
	case *syntax.Word:
		return x.Lit()
	case *syntax.BinaryArithm:
		return compactArithmText(x.X) + x.Op.String() + compactArithmText(x.Y)
	case *syntax.UnaryArithm:
		if x.Post {
			return compactArithmText(x.X) + x.Op.String()
		}
		return x.Op.String() + compactArithmText(x.X)
	case *syntax.ParenArithm:
		return "(" + compactArithmText(x.X) + ")"
	case *syntax.FlagsArithm:
		if x.Flags == nil {
			return compactArithmText(x.X)
		}
		return "(" + x.Flags.Value + ")" + compactArithmText(x.X)
	default:
		return subscriptText(expr)
	}
}

// wordHasDoubleQuote reports whether the word contains a double-quoted
// part — the form a `let` subscript takes once its surrounding `"…\"…\""`
// has been word-expanded a single time (`a[\" \"]` -> `a[" "]`).
func wordHasDoubleQuote(word *syntax.Word) bool {
	for _, part := range word.Parts {
		if _, ok := part.(*syntax.DblQuoted); ok {
			return true
		}
	}
	return false
}

// validNameRefTargetName reports whether s is a legal nameref target:
// a plain identifier or an array-element reference (`arr[idx]`). Mirrors
// interp.validNameRefTarget for the expand-side assignment path.
func validNameRefTargetName(s string) bool {
	if syntax.ValidName(s) {
		return true
	}
	_, idx, ok := nameRefArrayTarget(s)
	return ok && idx != ""
}

func nameRefArrayTarget(s string) (name, idx string, ok bool) {
	lb := strings.IndexByte(s, '[')
	if lb <= 0 || !strings.HasSuffix(s, "]") {
		return "", "", false
	}
	name = s[:lb]
	if !syntax.ValidName(name) {
		return "", "", false
	}
	return name, s[lb+1 : len(s)-1], true
}

func nameRefArrayTargetIndex(idx string) syntax.ArithmExpr {
	src := "x[" + idx + "]=_"
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	if file, err := parser.Parse(strings.NewReader(src), ""); err == nil && len(file.Stmts) == 1 {
		if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 1 {
			if call.Assigns[0].Index != nil {
				return call.Assigns[0].Index
			}
		}
	}
	return &syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{Value: idx},
	}}
}

func (cfg *Config) namesByPrefix(prefix string) []string {
	var names []string
	for name := range cfg.Env.Each {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
