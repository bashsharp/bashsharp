// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TODO(v4): the arithmetic APIs should return int64 for portability with 32-bit systems,
// even if Bash only supports native int sizes.

type ArithmError struct {
	Expr syntax.ArithmExpr
	Text string
	Err  error
	// Standalone marks an error from a top-level `$(( ))` / `$[ ]` word part (as
	// opposed to an array subscript or a nested operand). Bash 5.3 treats a
	// *parse* error in a standalone arithmetic expansion as fatal to the command,
	// so the runner fires that only for this shape.
	Standalone bool
}

func (e *ArithmError) Error() string { return e.Err.Error() }
func (e *ArithmError) Unwrap() error { return e.Err }

type arithLvalue struct {
	name       string
	word       *syntax.Word
	index      syntax.ArithmExpr
	indexValue int
	indexKey   string
	indexAssoc bool
	indexSet   bool
	fromString bool
}

// isAllDigits reports whether s is non-empty and consists entirely
// of ASCII digits (no sign, no separators).
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func minInt() int {
	return -int(^uint(0)>>1) - 1
}

func arithWordLit(expr syntax.ArithmExpr) string {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return ""
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return ""
	}
	return strings.TrimSpace(lit.Value)
}

func (cfg *Config) snapshotArithmParams(expr syntax.ArithmExpr) (map[*syntax.ParamExp]string, error) {
	values := make(map[*syntax.ParamExp]string)
	if expr == nil {
		return values, nil
	}
	var err error
	syntax.Walk(expr, func(node syntax.Node) bool {
		if err != nil {
			return false
		}
		pe, ok := node.(*syntax.ParamExp)
		if !ok {
			return true
		}
		if !pe.Dollar.IsValid() {
			return true
		}
		values[pe], err = Literal(cfg, &syntax.Word{Parts: []syntax.WordPart{pe}})
		return false
	})
	return values, err
}

func (cfg *Config) arithmLiteral(word *syntax.Word) (string, error) {
	if len(cfg.arithmParamValues) == 0 {
		return Literal(cfg, word)
	}
	parts, changed := cfg.arithmSnapshotParts(word.Parts)
	if !changed {
		return Literal(cfg, word)
	}
	wordCopy := *word
	wordCopy.Parts = parts
	return Literal(cfg, &wordCopy)
}

func (cfg *Config) arithmIndexedParamLiteral(word *syntax.Word) (string, bool, error) {
	if len(word.Parts) != 1 {
		return "", false, nil
	}
	pe, ok := word.Parts[0].(*syntax.ParamExp)
	if !ok || pe.Param == nil || pe.NestedParam != nil || pe.Index == nil ||
		pe.Slice != nil || pe.Repl != nil || pe.Exp != nil ||
		pe.Length || pe.Width || pe.Excl || pe.Names != 0 {
		return "", false, nil
	}
	name := pe.Param.Value
	vr := cfg.Env.Get(name)
	if n, v := vr.Resolve(cfg.Env); n != "" {
		name, vr = n, v
	}
	if cfg.NoUnset && !vr.Declared() {
		return "", true, UnsetParameterError{Name: name, Message: "unbound variable"}
	}
	if vr.Kind == Associative {
		// Only a *bare* associative reference (`assoc[$key]`, no leading
		// `$`/`${…}`) needs this special handling. An explicit parameter
		// expansion (`${assoc[$key]}`, Dollar set) was already expanded
		// exactly once by snapshotArithmParams, so re-expanding the
		// subscript here would run an embedded `$(…)` key a second time
		// (e.g. `$(( ${A[$(echo … >&2)]} ))` emitting the side effect
		// twice). Leave that form to arithmLiteral, which reuses the
		// snapshot value.
		if pe.Dollar.IsValid() {
			return "", false, nil
		}
		// An associative array reference resolves its subscript to a
		// literal string key (one round of parameter/command expansion),
		// then looks the key up and yields its value for arithmetic
		// evaluation. Unlike an indexed subscript, the expanded text is
		// NOT re-parsed as arithmetic — so a key value that happens to
		// contain `]`, `[`, or `$(...)` (e.g. `assoc[$key]` where
		// `key='x],b[$(echo …)'`) is treated as an opaque key rather than
		// triggering further command substitution or subscript parsing.
		word, ok := pe.Index.(*syntax.Word)
		if !ok {
			return "", false, nil
		}
		switch nodeLit(word) {
		case "*", "@":
			return "", false, nil
		}
		// Expand the subscript with Literal directly (not arithmLiteral):
		// the latter snapshots a `$key` ParamExp into a bare `~` literal,
		// which Literal would then tilde-expand. Bash applies no tilde
		// expansion to the result of a subscript's parameter expansion, so
		// the key must match how the assignment path (resolveAritLvalue)
		// computed it — a plain Literal of the original index word.
		key, lerr := Literal(cfg, word)
		if lerr != nil {
			return "", true, lerr
		}
		if val, ok := vr.Map[key]; ok {
			return val, true, nil
		}
		return "0", true, nil
	}
	// An empty subscript (`y[$none]` where $none is unset expands to the
	// literal `y[]`) is rejected by bash's arithmetic evaluator while it
	// parses the array reference's name part — before any arithmetic is
	// evaluated. The evaluator parses the reference twice (once to resolve
	// the variable, once to read its value), so the diagnostic fires twice
	// and the reference evaluates to 0.
	if word, ok := pe.Index.(*syntax.Word); ok {
		if sub, lerr := cfg.arithmLiteral(word); lerr == nil && sub == "" {
			if cfg.OnBadArraySubscript != nil {
				cfg.OnBadArraySubscript(name + "[]")
				cfg.OnBadArraySubscript(name + "[]")
			}
			return "0", true, nil
		}
	}
	// An array subscript is its own arithmetic (sub)expression, not an
	// operand of the surrounding operator, so reset arithmInOperand: a
	// re-parsed `$( … )` subscript value reports only its own text.
	prevOperand := cfg.arithmInOperand
	cfg.arithmInOperand = false
	index, err := Arithm(cfg, pe.Index)
	cfg.arithmInOperand = prevOperand
	if err != nil {
		return "", true, err
	}
	switch vr.Kind {
	case Indexed:
		// A negative subscript counts from the end, as in ${a[-1]}.
		if index < 0 {
			indexes := vr.IndexedIndexes()
			if len(indexes) == 0 {
				if cfg.OnBadArraySubscript != nil {
					cfg.OnBadArraySubscript(name)
				}
				break
			}
			index = indexes[len(indexes)-1] + 1 + index
			if index < 0 {
				if cfg.OnBadArraySubscript != nil {
					cfg.OnBadArraySubscript(name)
				}
				break
			}
		}
		if vr.IndexedSet(index) {
			return vr.IndexedElem(index), true, nil
		}
	case String, NameRef:
		if index == 0 && vr.IsSet() {
			return vr.Str, true, nil
		}
	}
	return "0", true, nil
}

func (cfg *Config) arithmSnapshotParts(parts []syntax.WordPart) ([]syntax.WordPart, bool) {
	var changed bool
	out := make([]syntax.WordPart, len(parts))
	for i, part := range parts {
		switch part := part.(type) {
		case *syntax.ParamExp:
			if val, ok := cfg.arithmParamValues[part]; ok {
				out[i] = &syntax.Lit{Value: val}
				changed = true
				continue
			}
		case *syntax.DblQuoted:
			dqParts, ok := cfg.arithmSnapshotParts(part.Parts)
			if ok {
				dq := *part
				dq.Parts = dqParts
				out[i] = &dq
				changed = true
				continue
			}
		}
		out[i] = part
	}
	return out, changed
}

// containsArithOp reports whether s contains a character that would
// make it an arithmetic expression rather than a bare identifier or
// number literal. Used to decide whether the string form of an arith
// operand needs to be re-parsed (`let "jv *= 2"`).
func containsArithOp(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '+', '-', '*', '/', '%', '<', '>', '=', '!',
			'&', '|', '^', '~', '?', ':', '(', ')', '[', ']', ' ', '\t':
			return true
		}
	}
	return false
}

func arithmSingleQuotedText(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		sq, ok := part.(*syntax.SglQuoted)
		if !ok || sq.Dollar {
			return "", false
		}
		b.WriteString(sq.Value)
	}
	return b.String(), true
}

func arithmRawWordPart(part syntax.WordPart) (string, bool) {
	switch part := part.(type) {
	case *syntax.Lit:
		return part.Value, true
	case *syntax.ParamExp:
		if part.Param != nil && part.NestedParam == nil && part.Index == nil && part.Slice == nil &&
			part.Repl == nil && part.Exp == nil && part.Length == false && part.Width == false &&
			part.Excl == false && part.Names == 0 {
			return "$" + part.Param.Value, true
		}
	}
	return "", false
}

func arithmDoubleQuotedText(word *syntax.Word) (string, bool, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false, false
	}
	var b strings.Builder
	var hasParam bool
	for _, part := range word.Parts {
		dq, ok := part.(*syntax.DblQuoted)
		if !ok || dq.Dollar {
			return "", false, false
		}
		for _, dqPart := range dq.Parts {
			text, ok := arithmRawWordPart(dqPart)
			if !ok {
				return "", false, false
			}
			_, isParam := dqPart.(*syntax.ParamExp)
			hasParam = hasParam || isParam
			b.WriteString(text)
		}
	}
	return b.String(), hasParam, true
}

func arithmWordHasParam(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.ParamExp:
			return true
		case *syntax.DblQuoted:
			for _, dqPart := range part.Parts {
				if _, ok := dqPart.(*syntax.ParamExp); ok {
					return true
				}
			}
		}
	}
	return false
}

func arithmQuoteExpandedParamValue(s string) string {
	if !strings.ContainsAny(s, `\[]$`+"`") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '[', ']', '$', '`':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func arithmQuoteMalformedIndexText(s string) string {
	if strings.ContainsAny(s, "$`") {
		return s
	}
	if !strings.ContainsAny(s, `\[]`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func arithmNameStart(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

func arithmNameContinue(b byte) bool {
	return arithmNameStart(b) || ('0' <= b && b <= '9')
}

func (cfg *Config) expandArithmDiagnosticText(s string) string {
	if !strings.ContainsAny(s, "$`") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) || !arithmNameStart(s[i+1]) {
			b.WriteByte(s[i])
			continue
		}
		j := i + 2
		for j < len(s) && arithmNameContinue(s[j]) {
			j++
		}
		name := s[i+1 : j]
		if vr := cfg.Env.Get(name); vr.IsSet() {
			b.WriteString(arithmQuoteExpandedParamValue(vr.String()))
		}
		i = j - 1
	}
	return b.String()
}

// stripArithmCTLESC removes CTLESC escape bytes (control bytes below
// 0x20) from s, mirroring bash's dequoting of arithmetic operand text.
// Other bytes — including 0x7f (DEL) — survive. The second return is
// true when s contained any byte that is not a valid arithmetic-name
// character (so the operand needs the invalid-token diagnostic).
func stripArithmCTLESC(s string) (string, bool) {
	bad := false
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 {
			bad = true
			continue
		}
		if c == 0x7f {
			bad = true
		}
		b.WriteByte(c)
	}
	return b.String(), bad
}

// leadingArithmName returns the longest prefix of s that forms a valid
// arithmetic identifier ([A-Za-z_][A-Za-z0-9_]*), or "" if s does not
// start with a name character.
func leadingArithmName(s string) string {
	if len(s) == 0 || !arithmNameStart(s[0]) {
		return ""
	}
	i := 1
	for i < len(s) && arithmNameContinue(s[i]) {
		i++
	}
	return s[:i]
}

func arithmBadSingleQuotedText(expanded string) bool {
	return strings.Contains(expanded, `\],`) && strings.Contains(expanded, `\[`)
}

func (cfg *Config) expandArithmText(s string) (string, error) {
	if !strings.ContainsAny(s, "$`") {
		return s, nil
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(": "+s), "")
	if err != nil || len(file.Stmts) != 1 {
		return s, nil
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) < 2 {
		return s, nil
	}
	args := make([]string, 0, len(call.Args)-1)
	for _, arg := range call.Args[1:] {
		val, err := Literal(cfg, arg)
		if err != nil {
			return "", err
		}
		args = append(args, val)
	}
	return strings.Join(args, " "), nil
}

func expandedAssocSubscriptError(s string) error {
	idx := strings.Index(s, "],")
	if idx < 0 {
		return nil
	}
	token := s[idx:]
	if !strings.Contains(token, "[") {
		return nil
	}
	if end := strings.LastIndex(token, "]"); end > 0 {
		token = token[:end]
	}
	return &ArithmError{
		Text: arithmQuoteMalformedIndexText(s),
		Err: fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s\")",
			arithmQuoteMalformedIndexText(token)),
	}
}

func arithmInvalidSubscriptChain(s string) (string, bool) {
	first := strings.IndexByte(s, '[')
	if first < 0 {
		return "", false
	}
	if isAllDigits(s[:first]) {
		return s[first:], true
	}
	depth := 0
	for i := first; i < len(s); i++ {
		switch s[i] {
		case '[':
			if depth == 0 && i == first+1 {
				return s[i:], true
			}
			depth++
		case ']':
			if depth > 0 {
				depth--
				if depth == 0 && i+1 < len(s) && s[i+1] == '[' {
					return s[i+1:], true
				}
			}
		}
	}
	return "", false
}

func Arithm(cfg *Config, expr syntax.ArithmExpr) (int, error) {
	cfg = prepareConfig(cfg)
	if cfg.arithmParamValues != nil {
		return arithm(cfg, expr)
	}
	_, topWord := expr.(*syntax.Word)
	if !cfg.arithmDynamicReparse && !cfg.LetArithmetic && !topWord {
		text, dynamic, err := cfg.dynamicArithmText(expr)
		if err != nil {
			return 0, err
		}
		if dynamic {
			file, perr := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("(("+text+"))"), "")
			if perr != nil {
				return 0, &ArithmError{Text: text, Err: arithmParseError(text, perr)}
			}
			if len(file.Stmts) == 1 {
				if ac, ok := file.Stmts[0].Cmd.(*syntax.ArithmCmd); ok && ac.X != nil {
					prev := cfg.arithmDynamicReparse
					cfg.arithmDynamicReparse = true
					n, err := Arithm(cfg, ac.X)
					cfg.arithmDynamicReparse = prev
					if err != nil {
						if _, ok := err.(*ArithmError); ok {
							return 0, err
						}
						return 0, &ArithmError{Expr: ac.X, Text: text, Err: err}
					}
					return n, nil
				}
			}
		}
	}
	values, err := cfg.snapshotArithmParams(expr)
	if err != nil {
		return 0, err
	}
	prev := cfg.arithmParamValues
	cfg.arithmParamValues = values
	defer func() { cfg.arithmParamValues = prev }()
	return arithm(cfg, expr)
}

func (cfg *Config) dynamicArithmText(expr syntax.ArithmExpr) (string, bool, error) {
	switch expr := expr.(type) {
	case nil:
		return "", false, nil
	case *syntax.Word:
		if src, ok := cfg.arithmIndexedWordSource(expr); ok {
			return src, false, nil
		}
		if dynamicArithmWord(expr) {
			str, err := Literal(cfg, expr)
			return str, true, err
		}
		return arithmWordSource(expr), false, nil
	case *syntax.ParenArithm:
		x, dyn, err := cfg.dynamicArithmText(expr.X)
		if err != nil {
			return "", false, err
		}
		return "(" + x + ")", dyn, nil
	case *syntax.UnaryArithm:
		x, dyn, err := cfg.dynamicArithmText(expr.X)
		if err != nil {
			return "", false, err
		}
		if expr.Post {
			return x + expr.Op.String(), dyn, nil
		}
		return expr.Op.String() + x, dyn, nil
	case *syntax.BinaryArithm:
		x, dynX, err := cfg.dynamicArithmText(expr.X)
		if err != nil {
			return "", false, err
		}
		y, dynY, err := cfg.dynamicArithmText(expr.Y)
		if err != nil {
			return "", false, err
		}
		return x + " " + expr.Op.String() + " " + y, dynX || dynY, nil
	default:
		panic(fmt.Sprintf("unexpected arithm expr: %T", expr))
	}
}

func (cfg *Config) arithmIndexedWordSource(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false
	}
	pe, ok := word.Parts[0].(*syntax.ParamExp)
	if !ok || pe.Param == nil || pe.NestedParam != nil || pe.Index == nil ||
		pe.Slice != nil || pe.Repl != nil || pe.Exp != nil ||
		pe.Length || pe.Width || pe.Excl || pe.Names != 0 {
		return "", false
	}
	var index string
	if word, ok := pe.Index.(*syntax.Word); ok {
		index = arithmWordSource(word)
	} else {
		index = arithmExprStaticText(pe.Index)
	}
	return pe.Param.Value + "[" + index + "]", true
}

func dynamicArithmWord(word *syntax.Word) bool {
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.ParamExp:
			if part.Dollar.IsValid() {
				return true
			}
		case *syntax.DblQuoted:
			return true
		case *syntax.CmdSubst, *syntax.ArithmExp:
			return true
		}
	}
	return false
}

func arithmWordSource(word *syntax.Word) string {
	if word == nil {
		return ""
	}
	if len(word.Parts) == 1 {
		switch part := word.Parts[0].(type) {
		case *syntax.Lit:
			return part.Value
		case *syntax.SglQuoted:
			return "'" + part.Value + "'"
		case *syntax.DblQuoted:
			return `"` + arithmWordPartsSource(part.Parts) + `"`
		}
	}
	return arithmWordPartsSource(word.Parts)
}

func arithmExprStaticText(expr syntax.ArithmExpr) string {
	switch expr := expr.(type) {
	case nil:
		return ""
	case *syntax.Word:
		return arithmWordSource(expr)
	case *syntax.ParenArithm:
		return "(" + arithmExprStaticText(expr.X) + ")"
	case *syntax.UnaryArithm:
		if expr.Post {
			return arithmExprStaticText(expr.X) + expr.Op.String()
		}
		return expr.Op.String() + arithmExprStaticText(expr.X)
	case *syntax.BinaryArithm:
		return arithmExprStaticText(expr.X) + " " + expr.Op.String() + " " + arithmExprStaticText(expr.Y)
	}
	return ""
}

func arithmWordPartsSource(parts []syntax.WordPart) string {
	var b strings.Builder
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			b.WriteByte('\'')
			b.WriteString(part.Value)
			b.WriteByte('\'')
		case *syntax.DblQuoted:
			b.WriteByte('"')
			b.WriteString(arithmWordPartsSource(part.Parts))
			b.WriteByte('"')
		case *syntax.ParamExp:
			if part.Param != nil && part.Dollar.IsValid() {
				b.WriteByte('$')
				b.WriteString(part.Param.Value)
			}
		}
	}
	return b.String()
}

func arithm(cfg *Config, expr syntax.ArithmExpr) (int, error) {
	switch expr := expr.(type) {
	case nil:
		return 0, nil
	case *syntax.Word:
		if arithEscapedEmptyQuotedWord(expr) {
			return 0, &ArithmError{
				Text: `"" `,
				Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is \"\"\" \")"),
			}
		}
		str, ok, err := cfg.arithmIndexedParamLiteral(expr)
		if err != nil {
			return 0, err
		}
		if !ok {
			str, err = cfg.arithmLiteral(expr)
			if err != nil {
				return 0, err
			}
		}
		if dqText, hasParam, ok := arithmDoubleQuotedText(expr); ok && hasParam && containsArithOp(dqText) {
			str = dqText
		}
		if sqText, ok := arithmSingleQuotedText(expr); ok {
			expanded := cfg.expandArithmDiagnosticText(sqText)
			trimmed := strings.TrimSpace(sqText)
			quotedNumeric := !cfg.LetArithmetic && trimmed != "" && trimmed[0] >= '0' && trimmed[0] <= '9'
			// A single-quoted operand is never a valid arithmetic token.
			// Bash errors on it in genuine expression contexts — the
			// operand of an operator (`(( v = 'y' ))`, `'1' + 2`) — but an
			// array subscript coerces a quoted name to 0 (`a=(['x']=b)`),
			// so restrict the bare-name case to arithmInOperand. The
			// numeric/bad-text/set-name cases bash rejects everywhere.
			if arithmBadSingleQuotedText(expanded) || strings.Contains(sqText, "++") ||
				strings.Contains(sqText, "--") || quotedNumeric ||
				(syntax.ValidName(sqText) && cfg.Env.Get(sqText).IsSet()) ||
				(syntax.ValidName(sqText) && cfg.arithmInOperand) {
				text := "'" + expanded + "'"
				arithText := text
				if syntax.ValidName(sqText) && cfg.Env.Get(sqText).IsSet() {
					arithText += " "
				}
				if cfg.arithmInOperand {
					return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is \"%s\")", text+" ")
				}
				return 0, &ArithmError{
					Text: arithText,
					Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is \"%s\")", text+" "),
				}
			}
		}
		// A Word operand that expands to text containing control bytes
		// (e.g. an array subscript like `[$'x\001y\177z']`) is not a valid
		// arithmetic token. bash removes the CTLESC escape bytes (the
		// `\001` here), then its arithmetic lexer reads a leading
		// identifier; the remaining byte that can neither continue a name
		// nor form an operator (the literal `\177`) is reported as an
		// invalid operator. The echoed expression keeps the surviving
		// control bytes (`xy\177z`) and the error token is the unparsable
		// remainder (`\177z`).
		if cleaned, ctrl := stripArithmCTLESC(str); ctrl {
			name := leadingArithmName(cleaned)
			if rest := cleaned[len(name):]; name != "" && rest != "" {
				return 0, &ArithmError{
					Text: cleaned,
					Err: fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s\")",
						rest),
				}
			}
		}
		// recursively fetch vars
		i := 0
		for syntax.ValidName(str) {
			vr := cfg.Env.Get(str)
			if n, resolved := vr.Resolve(cfg.Env); n != "" {
				vr = resolved
			}
			if !vr.IsSet() {
				if cfg.NoUnset {
					return 0, UnsetParameterError{Name: str, Message: "unbound variable"}
				}
				break
			}
			if vr.Kind == Associative {
				str = vr.Map["0"]
				break
			}
			val := vr.String()
			if val == "" {
				break
			}
			if i++; i >= maxNameRefDepth {
				return 0, fmt.Errorf("%s: expression recursion level exceeded (error token is %q)", str, str)
			}
			str = val
		}
		if strings.TrimSpace(str) == "}" {
			return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", "}")
		}
		if token, ok := arithmInvalidSubscriptChain(str); ok {
			err := fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s \")",
				token)
			if cfg.arithmInOperand {
				return 0, err
			}
			return 0, &ArithmError{
				Text: str + " ",
				Err:  err,
			}
		}
		// Bash re-parses the literal text of a Word-shaped arith
		// operand as an arithmetic expression when it contains
		// operators — `let "jv *= 2"` quotes the whole expression
		// in a Word, but we still need to evaluate it.
		if containsArithOp(str) {
			if err := expandedAssocSubscriptError(str); err != nil {
				return 0, err
			}
			// Check for literal `$` tokens in single-quoted text before re-parsing.
			// Single-quoted strings like '$iv' have literal $ that should error.
			if sqText, ok := arithmSingleQuotedText(expr); ok {
				if token := literalDollarArithmToken(sqText); token != "" {
					return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", token)
				}
			}
			file, perr := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("(("+str+"))"), "")
			if perr != nil {
				return 0, &ArithmError{Text: str, Err: arithmParseError(str, perr)}
			}
			if perr == nil && len(file.Stmts) == 1 {
				if ac, ok := file.Stmts[0].Cmd.(*syntax.ArithmCmd); ok && ac.X != nil {
					// Avoid infinite recursion when the re-parse
					// produces the same inert Word back.
					if word, isWord := ac.X.(*syntax.Word); !isWord || arithmWordHasParam(word) {
						prev := cfg.arithmParamValues
						cfg.arithmParamValues = nil
						n, err := Arithm(cfg, ac.X)
						cfg.arithmParamValues = prev
						if err != nil {
							if _, ok := err.(*ArithmError); ok {
								return 0, err
							}
							return 0, &ArithmError{Expr: ac.X, Text: str, Err: err}
						}
						return n, nil
					}
				}
			}
		}
		if token := literalDollarArithmToken(str); token != "" {
			// For a standalone operand (top-level expression or an array
			// subscript) bash reports the operand's own expanded text. For
			// a direct operand of an operator (`jv += $iv`) it reports the
			// surrounding expression instead, so leave Text empty and let
			// the caller print the full source expression.
			if cfg.arithmInOperand {
				return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", token)
			}
			return 0, &ArithmError{
				Expr: expr,
				Text: str,
				Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", token),
			}
		}
		n, err := atoiCheck(str)
		if err != nil {
			return 0, err
		}
		return int(n), nil
	case *syntax.ParenArithm:
		return Arithm(cfg, expr.X)
	case *syntax.UnaryArithm:
		prevOperand := cfg.arithmInOperand
		cfg.arithmInOperand = true
		defer func() { cfg.arithmInOperand = prevOperand }()
		switch expr.Op {
		case syntax.Inc, syntax.Dec:
			// Bash 5.3 distinguishes:
			//   - `7++` / `7--` (literal number) — treats as a
			//     parse-time syntax error: "arithmetic syntax
			//     error: operand expected (error token is "+ ")".
			//     The parser splits `++` into `+ +`; the trailing
			//     `+` is a unary operator missing its operand.
			//   - `--x++` (compound expression) — "assignment
			//     requires lvalue".
			//   - other non-name lvalues — "attempted assignment to
			//     non-variable".
			op := "++"
			tail := "+ "
			if expr.Op == syntax.Dec {
				op = "--"
				tail = "- "
			}
			lval, ok := arithLvalueFrom(expr.X)
			if !ok {
				if !expr.Post {
					if inner, ok := expr.X.(*syntax.UnaryArithm); ok && (inner.Op == syntax.Plus || inner.Op == syntax.Minus) {
						return Arithm(cfg, expr.X)
					}
				}
				badOp := op
				if inner, ok := expr.X.(*syntax.UnaryArithm); ok {
					switch inner.Op {
					case syntax.Inc:
						badOp = "++"
					case syntax.Dec:
						badOp = "--"
					}
				}
				return 0, fmt.Errorf("%s: assignment requires lvalue (error token is %q)", badOp, badOp+" ")
			}
			if word, ok := expr.X.(*syntax.Word); ok {
				if sqText, ok := arithmSingleQuotedText(word); ok {
					expanded := cfg.expandArithmDiagnosticText(sqText)
					// Quotes survive in arithmetic source text. They make
					// the whole increment operand invalid, even when the
					// text inside names an existing array element.
					text := "'" + expanded + "'" + op
					return 0, &ArithmError{
						Text: text,
						Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is \"%s\")", text+" "),
					}
				}
			}
			if word, ok := expr.X.(*syntax.Word); ok && arithMissingRHS(word) {
				return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tail)
			}
			if lval.name != "" && isAllDigits(lval.name) {
				if !expr.Post {
					return Arithm(cfg, expr.X)
				}
				return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tail)
			}
			lval, err := cfg.resolveAritLvalueName(lval)
			if err != nil {
				return 0, err
			}
			if !syntax.ValidName(lval.name) {
				return 0, fmt.Errorf("attempted assignment to non-variable (error token is %q)", op)
			}
			lval, err = cfg.resolveAritLvalue(lval)
			if err != nil {
				return 0, err
			}
			old, err := cfg.getAritLvalue(lval)
			if err != nil {
				return 0, err
			}
			val := old
			if expr.Op == syntax.Inc {
				val++
			} else {
				val--
			}
			if err := cfg.setAritLvalue(lval, val); err != nil {
				return 0, err
			}
			if expr.Post {
				return int(old), nil
			}
			return int(val), nil
		}
		val, err := Arithm(cfg, expr.X)
		if err != nil {
			return 0, err
		}
		switch expr.Op {
		case syntax.Not:
			return oneIf(val == 0), nil
		case syntax.BitNegation:
			return ^val, nil
		case syntax.Plus:
			return val, nil
		case syntax.Minus:
			if arithWordLit(expr.X) == "9223372036854775808" {
				return minInt(), nil
			}
			return -val, nil
		default:
			return 0, fmt.Errorf("unsupported unary arithmetic operator: %q", expr.Op)
		}
	case *syntax.BinaryArithm:
		prevOperand := cfg.arithmInOperand
		cfg.arithmInOperand = true
		defer func() { cfg.arithmInOperand = prevOperand }()
		switch expr.Op {
		case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn,
			syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
			syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn,
			syntax.ShlAssgn, syntax.ShrAssgn:
			return cfg.assgnArit(expr)
		case syntax.TernQuest: // TernColon can't happen here
			cond, err := Arithm(cfg, expr.X)
			if err != nil {
				return 0, err
			}
			b2 := expr.Y.(*syntax.BinaryArithm) // must have Op==TernColon
			if word, _ := b2.X.(*syntax.Word); arithMissingRHS(word) {
				return 0, fmt.Errorf("expression expected")
			}
			if word, _ := b2.Y.(*syntax.Word); arithMissingRHS(word) {
				if b2.OpPos == expr.OpPos {
					return 0, fmt.Errorf("`:' expected for conditional expression")
				}
				return 0, fmt.Errorf("expression expected")
			}
			if cond != 0 {
				return Arithm(cfg, b2.X)
			}
			return Arithm(cfg, b2.Y)
		case syntax.AndArit:
			left, err := Arithm(cfg, expr.X)
			if err != nil {
				return 0, err
			}
			if left == 0 {
				return 0, nil
			}
			right, err := Arithm(cfg, expr.Y)
			if err != nil {
				return 0, err
			}
			return oneIf(right != 0), nil
		case syntax.OrArit:
			left, err := Arithm(cfg, expr.X)
			if err != nil {
				return 0, err
			}
			if left != 0 {
				return 1, nil
			}
			right, err := Arithm(cfg, expr.Y)
			if err != nil {
				return 0, err
			}
			return oneIf(right != 0), nil
		case syntax.XorBool:
			left, err := Arithm(cfg, expr.X)
			if err != nil {
				return 0, err
			}
			right, err := Arithm(cfg, expr.Y)
			if err != nil {
				return 0, err
			}
			return oneIf((left != 0) != (right != 0)), nil
		case syntax.HashArit:
			token := arithmExprStaticText(expr.Y)
			// bash echoes the offending token verbatim between literal
			// double quotes; it does not escape control bytes. A token
			// that runs to end of line (`2 # comment`) therefore keeps
			// its trailing newline rather than printing `\n`.
			return 0, &ArithmError{
				Expr: expr,
				Err:  fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s\")", token),
			}
		}
		left, err := Arithm(cfg, expr.X)
		if err != nil {
			return 0, err
		}
		if arithEmptyDoubleQuotedWord(expr.Y) {
			return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is \"\"\"\")")
		}
		right, err := Arithm(cfg, expr.Y)
		if err != nil {
			return 0, err
		}
		if expr.Op == syntax.Pow && right < 0 {
			return 0, fmt.Errorf("exponent less than 0")
		}
		return binArit(expr.Op, left, right)
	default:
		panic(fmt.Sprintf("unexpected arithm expr: %T", expr))
	}
}

func arithLvalueFrom(expr syntax.ArithmExpr) (arithLvalue, bool) {
	w, ok := expr.(*syntax.Word)
	if !ok {
		return arithLvalue{}, false
	}
	if len(w.Parts) == 1 {
		if pe, ok := w.Parts[0].(*syntax.ParamExp); ok && pe.Param != nil && pe.NestedParam == nil {
			return arithLvalue{name: pe.Param.Value, index: pe.Index}, true
		}
	}
	return arithLvalue{name: w.Lit(), word: w}, true
}

func (cfg *Config) resolveAritLvalueName(lval arithLvalue) (arithLvalue, error) {
	if lval.word == nil {
		return lval, nil
	}
	name, err := LiteralWithQuoteRemoval(cfg, lval.word)
	if err != nil {
		return lval, err
	}
	if parsed, ok := arithLvalueFromString(name); ok {
		return parsed, nil
	}
	lval.name = name
	return lval, nil
}

func arithLvalueFromString(s string) (arithLvalue, bool) {
	if !strings.HasSuffix(s, "]") {
		return arithLvalue{}, false
	}
	open := strings.IndexByte(s, '[')
	if open <= 0 || !syntax.ValidName(s[:open]) {
		return arithLvalue{}, false
	}
	expr, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Arithmetic(strings.NewReader(s[open+1 : len(s)-1]))
	if err != nil {
		return arithLvalue{}, false
	}
	return arithLvalue{name: s[:open], index: expr, fromString: true}, true
}

func arithEscapedQuotedIndex(expr syntax.ArithmExpr) (int, bool) {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return 0, false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(lit.Value, `\"`) || !strings.HasSuffix(lit.Value, `\"`) {
		return 0, false
	}
	return 0, true
}

func (cfg *Config) arithLetEscapedQuotedIndex(expr syntax.ArithmExpr) (string, bool, error) {
	if !cfg.LetArithmetic {
		return "", false, nil
	}
	word, ok := expr.(*syntax.Word)
	if !ok {
		return "", false, nil
	}
	text, err := Literal(cfg, word)
	if err != nil {
		return "", false, err
	}
	switch {
	case strings.HasPrefix(text, `\"`) && strings.HasSuffix(text, `\"`):
		return strings.TrimSuffix(strings.TrimPrefix(text, `\"`), `\"`), true, nil
	case strings.HasPrefix(text, `"`) && strings.HasSuffix(text, `"`):
		return strings.TrimSuffix(strings.TrimPrefix(text, `"`), `"`), true, nil
	}
	return "", false, nil
}

func arithEmptyQuotedIndex(expr syntax.ArithmExpr) bool {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return false
	}
	dq, ok := word.Parts[0].(*syntax.DblQuoted)
	return ok && len(dq.Parts) == 0
}

func arithEscapedEmptyQuotedWord(expr syntax.ArithmExpr) bool {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	return ok && lit.Value == `\"\"`
}

func arithEmptyDoubleQuotedWord(expr syntax.ArithmExpr) bool {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return false
	}
	dq, ok := word.Parts[0].(*syntax.DblQuoted)
	return ok && len(dq.Parts) == 0
}

func (cfg *Config) getAritLvalue(lval arithLvalue) (int64, error) {
	if lval.index == nil {
		vr := cfg.Env.Get(lval.name)
		if cfg.NoUnset && !vr.Declared() {
			return 0, UnsetParameterError{Name: lval.name, Message: "unbound variable"}
		}
		return atoi(vr.String()), nil
	}
	if !lval.indexSet {
		var err error
		lval, err = cfg.resolveAritLvalue(lval)
		if err != nil {
			return 0, err
		}
	}
	vr := cfg.Env.Get(lval.name)
	switch vr.Kind {
	case Associative:
		if lval.indexAssoc {
			return atoi(vr.Map[lval.indexKey]), nil
		}
	case Indexed:
		if vr.IndexedSet(lval.indexValue) {
			return atoi(vr.IndexedElem(lval.indexValue)), nil
		}
	case String, NameRef:
		if lval.indexValue == 0 {
			return atoi(vr.Str), nil
		}
	}
	return 0, nil
}

func (cfg *Config) resolveAritLvalue(lval arithLvalue) (arithLvalue, error) {
	if lval.index == nil || lval.indexSet {
		return lval, nil
	}
	if vr := cfg.Env.Get(lval.name); vr.Kind == Associative {
		word, ok := lval.index.(*syntax.Word)
		if !ok {
			return lval, fmt.Errorf("bad array subscript")
		}
		// With `assoc_expand_once` set, a `let` subscript is expanded
		// only once: quote characters that survive that single word
		// expansion are kept verbatim in the key rather than being
		// quote-removed a second time, so `["\" \""]` and `[" "]` are
		// distinct keys. The surviving quotes reach us in two shapes,
		// both of whose keys are the subscript's single double-quoted
		// expansion (`\"` -> literal `"`, parameters expanded once):
		//
		//   - a re-parsed double-quoted word (`let "a[\" \"]=…"`, whose
		//     str `a[" "]` re-parses to the index `" "`): its quotes are
		//     syntactic, so the printed source text is the literal key.
		//   - an escaped-quote operand (`let "a[\"$v\"]=…"`, kept raw as
		//     the index `\"$v\"`): re-expanding it in a double-quoted
		//     context turns `\"` into `"` and expands `$v` exactly once.
		if cfg.LetArithmetic && cfg.AssocExpandOnce {
			key := ""
			if wordHasDoubleQuote(word) {
				key = subscriptText(word)
			} else {
				quoted := &syntax.Word{Parts: []syntax.WordPart{
					&syntax.DblQuoted{Parts: word.Parts},
				}}
				var err error
				if key, err = Literal(cfg, quoted); err != nil {
					return lval, err
				}
			}
			lval.indexKey = key
			lval.indexAssoc = true
			lval.indexSet = true
			return lval, nil
		}
		key, quoted, err := "", false, error(nil)
		if !lval.fromString {
			key, quoted, err = cfg.arithLetEscapedQuotedIndex(lval.index)
		}
		if err != nil {
			return lval, err
		}
		if !quoted {
			key, err = Literal(cfg, word)
		}
		if err != nil {
			return lval, err
		}
		lval.indexKey = key
		lval.indexAssoc = true
		lval.indexSet = true
		return lval, nil
	}
	if index, ok := arithEscapedQuotedIndex(lval.index); ok && !lval.fromString {
		lval.indexValue = index
		lval.indexSet = true
		return lval, nil
	}
	if !lval.fromString {
		if _, ok, err := cfg.arithLetEscapedQuotedIndex(lval.index); err != nil {
			return lval, err
		} else if ok {
			lval.indexValue = 0
			lval.indexSet = true
			return lval, nil
		}
	}
	if arithEmptyQuotedIndex(lval.index) && !lval.fromString && !cfg.LetArithmetic {
		return lval, fmt.Errorf("`%s[]': not a valid identifier", lval.name)
	}
	// The subscript is its own arithmetic (sub)expression, not an operand
	// of the surrounding operator (`a[$idx]++`), so reset arithmInOperand
	// to report a re-parsed $( … ) subscript by its own expanded text.
	prevOperand := cfg.arithmInOperand
	cfg.arithmInOperand = false
	index, err := Arithm(cfg, lval.index)
	cfg.arithmInOperand = prevOperand
	if err != nil {
		return lval, err
	}
	if index < 0 {
		vr := cfg.Env.Get(lval.name)
		if vr.Kind != Indexed {
			return lval, fmt.Errorf("bad array subscript")
		}
		index = indexedNegativeOffset(vr, index)
		if index < 0 {
			return lval, fmt.Errorf("bad array subscript")
		}
	}
	lval.indexValue = index
	lval.indexSet = true
	return lval, nil
}

func (cfg *Config) setAritLvalue(lval arithLvalue, val int64) error {
	if lval.index == nil {
		return cfg.envSet(lval.name, strconv.FormatInt(val, 10))
	}
	wenv, ok := cfg.Env.(WriteEnviron)
	if !ok {
		return fmt.Errorf("environment is read-only")
	}
	if !lval.indexSet {
		var err error
		lval, err = cfg.resolveAritLvalue(lval)
		if err != nil {
			return err
		}
	}
	vr := cfg.Env.Get(lval.name)
	if vr.Kind == Associative {
		if vr.Map == nil {
			vr.Map = make(map[string]string)
		} else {
			vr.Map = maps.Clone(vr.Map)
		}
		vr.Set = true
		vr.Map[lval.indexKey] = strconv.FormatInt(val, 10)
		return wenv.Set(lval.name, vr)
	}
	if lval.indexValue < 0 {
		return fmt.Errorf("bad array subscript")
	}
	if vr.Kind == String && vr.Str != "" {
		vr.List = []string{vr.Str}
		vr.ListSet = nil
	}
	vr.Kind = Indexed
	vr.Set = true
	vr.List = slices.Clone(vr.List)
	vr.ListSet = vr.CloneListSet()
	vr.ListMap = vr.CloneListMap()
	vr.SetIndexed(lval.indexValue, strconv.FormatInt(val, 10))
	return wenv.Set(lval.name, vr)
}

func oneIf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// atoi is like [strconv.ParseInt](s, BASE, 64), but it handles integer
// base prefixes according to bash-shell's rules, ignores errors, and
// trims whitespace.
//
// For more information about bash's integer base handling syntax,
// refer to the bash manual:
// https://www.man7.org/linux/man-pages/man1/bash.1.html
func atoi(s string) int64 {
	n, _ := atoiCheck(s)
	return n
}

func atoiCheck(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '.'); i >= 0 && i > 0 && s[0] >= '0' && s[0] <= '9' {
		return 0, fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is %q)", s[i:])
	}
	base := int64(10)
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base = 16
		s = s[2:]
	case strings.HasPrefix(s, "0"):
		base = 8
		s = s[1:]
		if strings.Contains(s, "#") {
			return 0, fmt.Errorf("invalid number (error token is %q)", orig)
		}
	default:
		baseStr, intStr, hasSep := strings.Cut(s, "#")
		if hasSep {
			var err error
			base, err = strconv.ParseInt(baseStr, 10, 8)
			if err != nil || base > 64 {
				return 0, fmt.Errorf("invalid arithmetic base (error token is %q)", orig)
			}
			if base < 2 {
				return 0, fmt.Errorf("invalid number (error token is %q)", orig)
			}
			if intStr == "" {
				return 0, fmt.Errorf("invalid integer constant (error token is %q)", orig)
			}
			if strings.Contains(intStr, "#") {
				return 0, fmt.Errorf("invalid number (error token is %q)", orig)
			}
			s = intStr
		}
	}
	// Bases 2-36 use Go's ParseInt (handles negative sign and case-
	// insensitive a-z). Bases 37-64 need bash's custom digit map:
	// 0-9 → 0-9, a-z → 10-35, A-Z → 36-61, @ → 62, _ → 63.
	if base <= 36 {
		n, err := strconv.ParseInt(s, int(base), 64)
		if err != nil && numericLiteralLike(orig) {
			return 0, fmt.Errorf("value too great for base (error token is %q)", orig)
		}
		if err != nil {
			if u, uerr := strconv.ParseUint(s, int(base), 64); uerr == nil {
				return int64(u), nil
			}
		}
		return n, nil
	}
	n, ok := bashBaseAtoi(s, base)
	if !ok {
		return 0, fmt.Errorf("value too great for base (error token is %q)", orig)
	}
	return n, nil
}

func numericLiteralLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "#") {
		return true
	}
	if s[0] >= '0' && s[0] <= '9' {
		for i := 1; i < len(s); i++ {
			if arithmNameStart(s[i]) || s[i] == '_' {
				return true
			}
		}
	}
	if len(s) > 1 && s[0] == '0' {
		for i := 1; i < len(s); i++ {
			if s[i] < '0' || s[i] > '7' {
				return true
			}
		}
	}
	return false
}

// ArithmCmdSubstToken reports the full text of a command substitution
// ($( … ) or `…`) found in an already-expanded arithmetic string, or ""
// if none. Callers re-evaluating expanded array subscripts use it to
// detect substitutions that bash treats as literal operands and never
// re-executes. Bare $name / ${ … } are left for normal evaluation, as
// they carry no execution and may legitimately reach arithmetic.
func ArithmCmdSubstToken(s string) string {
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '`':
			j := i + 1
			for j < len(s) && s[j] != '`' {
				j++
			}
			if j < len(s) {
				j++
			}
			return s[i:j]
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
			return arithmDollarSpan(s, i)
		}
	}
	return ""
}

func literalDollarArithmToken(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		j := i + 1
		// Check if followed by a valid variable name start character.
		if j < len(s) && arithmNameStart(s[j]) {
			// Consume the variable name.
			j++
			for j < len(s) && arithmNameContinue(s[j]) {
				j++
			}
			// If the variable name is followed by ']', this is a valid
			// array subscript expansion like $key in assoc[$key]++.
			// Skip it and continue looking for actual errors.
			if j < len(s) && s[j] == ']' {
				i = j - 1 // -1 because the for loop will do i++
				continue
			}
			// Otherwise, $name as a bare operand is an error.
			return s[i:j]
		}
		// Bare $ not followed by a name: for $( command substitution or
		// ${ parameter expansion, bash reports the *entire* construct as
		// the error token, since already-expanded text is re-parsed as a
		// literal arithmetic operand without re-running expansions.
		return arithmDollarSpan(s, i)
	}
	return ""
}

// arithmDollarSpan returns the text of a $-introduced construct starting
// at index i in s: the balanced $( ... ) for command substitution, the
// ${ ... } for parameter expansion, or a bare "$" otherwise.
func arithmDollarSpan(s string, i int) string {
	j := i + 1
	if j >= len(s) {
		return "$"
	}
	switch s[j] {
	case '(':
		depth := 0
		for j < len(s) {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return s[i : j+1]
				}
			}
			j++
		}
		return s[i:]
	case '{':
		for j < len(s) && s[j] != '}' {
			j++
		}
		if j < len(s) {
			j++
		}
		return s[i:j]
	}
	return "$"
}

func arithmParseError(s string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "must be followed by an expression"):
		tok := strings.TrimSpace(s)
		if idx := strings.LastIndexAny(s, "+-*/%<>=!&|^?:,"); idx >= 0 {
			tok = strings.TrimLeft(s[idx:], " \t")
		}
		return fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tok)
	case strings.Contains(msg, "without matching `((` with `))`"):
		tok := strings.TrimSpace(s)
		if fields := strings.Fields(tok); len(fields) > 0 {
			tok = fields[len(fields)-1]
		}
		return fmt.Errorf("missing `)' (error token is %q)", tok)
	default:
		return err
	}
}

// bashBaseAtoi parses an integer in bash's extended digit alphabet
// for bases 2-64. Returns 0 on any invalid digit; callers don't
// distinguish "invalid" from "zero" here, matching the silent
// behaviour of [atoi] for malformed input.
func bashBaseAtoi(s string, base int64) (int64, bool) {
	if len(s) == 0 {
		return 0, true
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'z':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = int64(c-'A') + 36
		case c == '@':
			d = 62
		case c == '_':
			d = 63
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
		n = n*base + d
	}
	if neg {
		n = -n
	}
	return n, true
}

func (cfg *Config) assgnArit(b *syntax.BinaryArithm) (int, error) {
	// Bash 5.3 accepts `7=4`, `(a)=4` at parse time and errors here
	// with "attempted assignment to non-variable". The arith parser
	// no longer rejects non-name lvalues so the for-loop tests can
	// reach this runtime path; surface the same wording bash uses.
	lval, ok := arithLvalueFrom(b.X)
	if !ok {
		return 0, fmt.Errorf("attempted assignment to non-variable")
	}
	if lval.word != nil {
		if text := arithmWordSource(lval.word); strings.Contains(text, "][") {
			if token, ok := arithmInvalidSubscriptChain(text); ok {
				errToken := token + " " + b.Op.String() + " " + arithmExprStaticText(b.Y) + " "
				return 0, &ArithmError{
					Text: arithmExprStaticText(b) + " ",
					Err: fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s\")",
						errToken),
				}
			}
		}
	}
	lval, err := cfg.resolveAritLvalueName(lval)
	if err != nil {
		return 0, err
	}
	if token, ok := arithmInvalidSubscriptChain(lval.name); ok {
		errToken := token + " " + b.Op.String() + " " + arithmExprStaticText(b.Y) + " "
		return 0, &ArithmError{
			Text: arithmExprStaticText(b) + " ",
			Err: fmt.Errorf("arithmetic syntax error: invalid arithmetic operator (error token is \"%s\")",
				errToken),
		}
	}
	if !syntax.ValidName(lval.name) {
		return 0, fmt.Errorf("attempted assignment to non-variable")
	}
	if word, ok := b.Y.(*syntax.Word); ok && arithMissingRHS(word) {
		return 0, fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", b.Op.String())
	}
	if b.Op == syntax.Assgn {
		arg, err := Arithm(cfg, b.Y)
		if err != nil {
			return 0, err
		}
		// Assigning through an as-yet-untargeted nameref retargets it;
		// an arithmetic result is never a valid identifier, so bash
		// rejects it ("((: `0': not a valid identifier").
		if lval.index == nil {
			if vr := cfg.Env.Get(lval.name); vr.Kind == NameRef && vr.Str == "" {
				target := strconv.FormatInt(int64(arg), 10)
				if !syntax.ValidName(target) {
					return 0, fmt.Errorf("`%s': not a valid identifier", target)
				}
			}
		}
		lval, err = cfg.resolveAritLvalue(lval)
		if err != nil {
			return 0, err
		}
		if err := cfg.setAritLvalue(lval, int64(arg)); err != nil {
			return 0, err
		}
		return arg, nil
	}
	lval, err = cfg.resolveAritLvalue(lval)
	if err != nil {
		return 0, err
	}
	val, err := cfg.getAritLvalue(lval)
	if err != nil {
		return 0, err
	}
	arg_, err := Arithm(cfg, b.Y)
	if err != nil {
		return 0, err
	}
	arg := int64(arg_)
	switch b.Op {
	case syntax.AddAssgn:
		val += arg
	case syntax.SubAssgn:
		val -= arg
	case syntax.MulAssgn:
		val *= arg
	case syntax.QuoAssgn:
		if arg == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		val /= arg
	case syntax.RemAssgn:
		if arg == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		val %= arg
	case syntax.AndAssgn:
		val &= arg
	case syntax.OrAssgn:
		val |= arg
	case syntax.XorAssgn:
		val ^= arg
	case syntax.ShlAssgn:
		val <<= uint(arg)
	case syntax.ShrAssgn:
		val >>= uint(arg)
	}
	if err := cfg.setAritLvalue(lval, val); err != nil {
		return 0, err
	}
	return int(val), nil
}

func arithMissingRHS(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	if len(word.Parts) != 1 {
		return false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	return ok && lit.Value == "" && lit.Pos() == lit.End()
}

func intPow(a, b int) int {
	p := 1
	for b > 0 {
		if b&1 != 0 {
			p *= a
		}
		b >>= 1
		a *= a
	}
	return p
}

func binArit(op syntax.BinAritOperator, x, y int) (int, error) {
	switch op {
	case syntax.Add:
		return x + y, nil
	case syntax.Sub:
		return x - y, nil
	case syntax.Mul:
		return x * y, nil
	case syntax.Quo:
		if y == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return x / y, nil
	case syntax.Rem:
		if y == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return x % y, nil
	case syntax.Pow:
		return intPow(x, y), nil
	case syntax.Eql:
		return oneIf(x == y), nil
	case syntax.Gtr:
		return oneIf(x > y), nil
	case syntax.Lss:
		return oneIf(x < y), nil
	case syntax.Neq:
		return oneIf(x != y), nil
	case syntax.Leq:
		return oneIf(x <= y), nil
	case syntax.Geq:
		return oneIf(x >= y), nil
	case syntax.And:
		return x & y, nil
	case syntax.Or:
		return x | y, nil
	case syntax.Xor:
		return x ^ y, nil
	case syntax.Shr:
		return x >> uint(y), nil
	case syntax.Shl:
		if y < 0 {
			y &= strconv.IntSize - 1
		}
		return x << uint(y), nil
	case syntax.AndArit:
		return oneIf(x != 0 && y != 0), nil
	case syntax.OrArit:
		return oneIf(x != 0 || y != 0), nil
	case syntax.Comma:
		// x is executed but its result discarded
		return y, nil
	default:
		return 0, fmt.Errorf("unsupported binary arithmetic operator: %q", op)
	}
}
