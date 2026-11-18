// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Parsing of the Bash++ function sites: the P3-A ("typed functions")
// command-position declarations — `func name(…) … { … }`, `defer f(x)` and
// the func-body `return` — and the P3-B additions, function LITERALS and
// VARIADIC parameter lists.
//
// THE FOUR LITERAL SITES, and the one that is not claimed. A literal appears
// wherever a value does: bound by `:=`, invoked immediately, deferred, or
// returned. Each is claimed on a prefix bash rejects — `f := func(`,
// `defer func(`, `return func(`, and a command-position `func(` whose
// parameter list is NOT empty. The exception is `func() { … }` at a command
// position, which is the bash definition of a function named `func` and stays
// shell: only the `(` after the matching `}` could tell the two apart, and
// that is unbounded lookahead. See [recognizeFuncLit].
//
// WHY THESE COMMIT FORWARD RATHER THAN TRANSACTIONALLY. The keyword-led
// declarations in bashpp_decl.go consume nothing on speculation because they
// are Class E — bash runs their near-misses as ordinary commands, so a wrong
// guess would change a working script. A `func name(` is different: it is two
// command words before a `(`, which bash rejects outright (Class R). There is
// therefore no working script to protect, and the region may be committed the
// moment the shape is recognized, exactly as `if`/`for`/`while` commit on their
// keyword. A malformed body becomes a parse error, which is what bash produces
// too.
//
// `defer` splits the way `:=` does: `defer cleanup` is a Class E command bash
// runs today and must stay shell, while `defer f(x)` is the Class R call form.
// Only the call form is claimed, and it is claimed transactionally through the
// same [Parser.bashppParenForm] machinery the bare call uses, so an unsupported
// argument shape rewinds to the shell rather than diagnosing.

// bashppFuncForm parses a Go-form function declaration once the ordinary call
// parser has collected `func name` and stopped at the opening `(`. It returns
// nil to leave the command to the shell, and otherwise a fully parsed
// [BashPPFuncDecl]; because the shape is Class R, a malformation past the
// commit point is reported as a parse error rather than rewound.
func (p *Parser) bashppFuncForm(ce *CallExpr) Command {
	if ce == nil || len(ce.Assigns) != 0 || p.tok != leftParen {
		return nil
	}
	if len(ce.Args) == 1 {
		kw := bashppBareLit(ce.Args[0])
		// Use source positions rather than p.spaced here: callExpr has already
		// advanced into the token, so the lexer spacing bit describes the next
		// boundary. A receiver requires whitespace after func; func(...) stays
		// the literal/classic-function ambiguity handled elsewhere.
		if kw == nil || kw.Value != "func" || p.pos.Offset() == kw.End().Offset() {
			return nil
		}
		// Bash also allows whitespace before the empty parentheses in a
		// classic `func () { ...; }` definition. Probe one token before
		// committing to a method receiver; a receiver cannot be empty.
		txn := p.beginBashPPTxn()
		p.next()
		empty := p.err == nil && p.tok == rightParen
		txn.rollback(p)
		if empty {
			return nil
		}
		return p.bashppMethodForm(kw)
	}
	if len(ce.Args) < 2 {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	name, typeParams, ok := p.bashppFuncTypeParams(ce.Args[1:])
	if kw == nil || kw.Value != "func" || name == nil || !bashppIsIdent(name.Value) || !ok {
		return nil
	}
	// Confirm the region opens here against the single decision table of
	// record before consuming anything, so the parser and the recognizer never
	// drift over what a func start site is.
	if RecognizeStartSite(kw.Value+" "+name.Value+"(").Site != StartFunc {
		return nil
	}

	txn := p.beginBashPPTxn()
	fd := &BashPPFuncDecl{Kw: kw, Name: name, TypeParams: typeParams}
	sig := p.bashppSignature("func " + name.Value)
	if sig.nearMiss {
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	p.bashppRegisterFunc(name.Value)
	fd.Params, fd.Results = sig.params, sig.results
	p.bashppMarkTypeParamFields(fd.Params, typeParams)
	p.bashppMarkTypeParamFields(fd.Results, typeParams)
	fd.Lparen, fd.Rparen = sig.lparen, sig.rparen
	fd.ResLparen, fd.ResRparen = sig.resLparen, sig.resRparen
	fd.Body = p.bashppFuncBody("func "+name.Value, sig.rparen, sig.params)
	return fd
}

func (p *Parser) bashppFuncTypeParams(words []*Word) (*Lit, []*BashPPTypeParam, bool) {
	return bashppFuncTypeParams(words)
}

func bashppFuncTypeParams(words []*Word) (*Lit, []*BashPPTypeParam, bool) {
	if len(words) == 1 {
		return bashppBareLit(words[0]), nil, true
	}
	firstText := bashppWordText(words[0])
	lastText := bashppWordText(words[len(words)-1])
	if !strings.Contains(firstText, "[") || !strings.HasSuffix(lastText, "]") {
		return nil, nil, false
	}
	nameText, rest, _ := strings.Cut(firstText, "[")
	if !bashppIsIdent(nameText) {
		return nil, nil, false
	}
	name := &Lit{ValuePos: words[0].Pos(), ValueEnd: posAddCol(words[0].Pos(), len(nameText)), Value: nameText}
	items := make([]*Lit, 0, len(words))
	if rest != "" {
		pos := posAddCol(words[0].Pos(), len(nameText)+1)
		items = append(items, &Lit{ValuePos: pos, ValueEnd: posAddCol(pos, len(rest)), Value: rest})
	}
	for _, word := range words[1 : len(words)-1] {
		text := bashppWordText(word)
		items = append(items, &Lit{ValuePos: word.Pos(), ValueEnd: word.End(), Value: text})
	}
	tail := strings.TrimSuffix(lastText, "]")
	if tail != "" {
		items = append(items, &Lit{ValuePos: words[len(words)-1].Pos(), ValueEnd: posAddCol(words[len(words)-1].End(), -1), Value: tail})
	}
	params, ok := bashppTypeParamList(items)
	return name, params, ok
}

// bashppMethodForm parses `func (r T) M(...)` and `func (r *T) M(...)`.
// The receiver opening is spaced from func, so it cannot collide with the
// classic shell definition `func() { ... }`.
func (p *Parser) bashppMethodForm(kw *Lit) Command {
	recv := &BashPPReceiver{Lparen: p.pos}
	p.next()
	nameWord := p.getWord()
	var typeWords []*Word
	for p.tok != rightParen && p.tok != _EOF && p.tok != _Newl {
		word := p.getWord()
		if word == nil {
			break
		}
		typeWords = append(typeWords, word)
	}
	recv.Name = bashppBareLit(nameWord)
	var typLit *Lit
	var typExpr BashPPTypeExpr
	if len(typeWords) > 0 {
		parts := make([]string, len(typeWords))
		for i, word := range typeWords {
			parts[i] = bashppWordText(word)
		}
		text := strings.Join(parts, " ")
		typLit = &Lit{ValuePos: typeWords[0].Pos(), ValueEnd: typeWords[len(typeWords)-1].End(), Value: text}
		typExpr = bashppTypeExprFromLit(typLit)
	}
	if recv.Name == nil || !bashppIsIdent(recv.Name.Value) || typLit == nil || typExpr == nil {
		p.posErr(recv.Lparen, "method receiver must be one name and one named type")
		return nil
	}
	if pointer, ok := typExpr.(*BashPPPointerType); ok {
		recv.Pointer = true
		typExpr = pointer.Element
	}
	named, ok := typExpr.(*BashPPNamedType)
	if !ok || !bashppIsIdent(named.Name.Value) {
		p.posErr(typLit.Pos(), "invalid method receiver type")
		return nil
	}
	recv.RecvType = named.Name
	for _, arg := range named.TypeArgs {
		param, ok := arg.ArgType.(*BashPPNamedType)
		if !ok || len(param.TypeArgs) > 0 || !bashppIsIdent(param.Name.Value) {
			p.posErr(arg.Pos(), "receiver type arguments must be identifiers")
			return nil
		}
		recv.TypeParams = append(recv.TypeParams, param.Name)
	}
	if p.tok != rightParen {
		p.followErr(recv.Lparen, "func (receiver", rightParen)
	}
	recv.Rparen = p.pos
	p.next()
	// The method name may be followed by its OWN type parameter list —
	// `func (b Box[T]) Get[U any](v U) U`. Go 1.27 allows a method to declare
	// type parameters independent of the receiver's, so the name is collected
	// as the same word run an ordinary generic `func name[U any](` produces
	// and handed to the one type-parameter reader both forms share. A method
	// with no list is the single-word case of that reader.
	var methodWords []*Word
	for p.tok != leftParen && p.tok != _EOF && p.tok != _Newl {
		word := p.getWord()
		if word == nil {
			break
		}
		methodWords = append(methodWords, word)
	}
	var method *Lit
	var methodParams []*BashPPTypeParam
	paramsOK := len(methodWords) > 0
	if paramsOK {
		method, methodParams, paramsOK = bashppFuncTypeParams(methodWords)
	}
	if !paramsOK || method == nil || !bashppIsIdent(method.Value) || p.tok != leftParen || p.spaced {
		p.posErr(recv.Rparen, "method declaration requires a name and signature")
		return nil
	}
	// A method type parameter may not reuse a receiver type parameter name:
	// both are in scope over the same signature and body, which is why Go
	// reports `T redeclared in this block` for the collision.
	for _, group := range methodParams {
		for _, name := range group.Names {
			for _, recvParam := range recv.TypeParams {
				if recvParam.Value == name.Value {
					p.posErr(name.Pos(), "method type parameter %s redeclares receiver type parameter", name.Value)
					return nil
				}
			}
		}
	}
	d := &BashPPFuncDecl{Kw: kw, Name: method, Receiver: recv, TypeParams: methodParams}
	p.bashppRegisterFunc(method.Value)
	sig := p.bashppSignature("func (" + recv.Name.Value + " " + typLit.Value + ") " + method.Value)
	d.Params, d.Results = sig.params, sig.results
	// Both scopes mark the signature: the receiver's parameters, whose
	// constraints come from the receiver type's own declaration, and the
	// method's own parameters, which carry their constraints here.
	scopeParams := make([]*BashPPTypeParam, 0, len(recv.TypeParams)+len(methodParams))
	for _, name := range recv.TypeParams {
		scopeParams = append(scopeParams, &BashPPTypeParam{Names: []*Lit{name}, Constraint: &BashPPNamedType{Name: &Lit{ValuePos: name.Pos(), ValueEnd: name.End(), Value: "any"}}})
	}
	scopeParams = append(scopeParams, methodParams...)
	p.bashppMarkTypeParamFields(d.Params, scopeParams)
	p.bashppMarkTypeParamFields(d.Results, scopeParams)
	d.Lparen, d.Rparen = sig.lparen, sig.rparen
	d.ResLparen, d.ResRparen = sig.resLparen, sig.resRparen
	d.Body = p.bashppFuncBody("method "+method.Value, sig.rparen, sig.params)
	return d
}

// bashppPointerMethodExpr parses the Class-R method expression
// `(*T).M(receiver, args...)` from a statement-opening parenthesis.
func (p *Parser) bashppPointerMethodExpr() Command {
	if p.tok != leftParen || p.r != '*' {
		return nil
	}
	txn := p.beginBashPPTxn()
	exprLparen := p.pos
	p.next()
	typeWord := p.getWord()
	typ := bashppBareLit(typeWord)
	if typ == nil || !strings.HasPrefix(typ.Value, "*") || !bashppIsIdent(strings.TrimPrefix(typ.Value, "*")) || p.tok != rightParen {
		txn.rollback(p)
		return nil
	}
	typeName := strings.TrimPrefix(typ.Value, "*")
	exprRparen := p.pos
	p.next()
	methodWord := p.getWord()
	method := bashppBareLit(methodWord)
	if method == nil || !strings.HasPrefix(method.Value, ".") || !bashppIsIdent(strings.TrimPrefix(method.Value, ".")) || p.tok != leftParen || p.spaced {
		txn.rollback(p)
		return nil
	}
	methodName := strings.TrimPrefix(method.Value, ".")
	callLparen := p.pos
	p.next()
	args, argNames, ellipsis, ok := p.bashppCallArgs()
	if !ok || p.tok != rightParen {
		txn.rollback(p)
		return nil
	}
	rparen := p.pos
	p.next()
	if !bashppCallTerminator(p.tok) {
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	typePos := posAddCol(typ.Pos(), 1)
	methodPos := posAddCol(method.Pos(), 1)
	return &BashPPCall{
		Fun: []*Lit{
			{ValuePos: typePos, ValueEnd: typ.End(), Value: typeName},
			{ValuePos: methodPos, ValueEnd: method.End(), Value: methodName},
		},
		Args: args, ArgNames: argNames, Ellipsis: ellipsis, PointerMethodExpr: true,
		Lparen: callLparen, Rparen: rparen, MethodExprLparen: exprLparen, MethodExprRparen: exprRparen,
		FuncLit: nil,
		// The outer expression parenthesis is represented by the boolean and
		// reconstructed by the printer; callLparen remains the argument list.
	}
}

// bashppRegisterFunc records name as a callable Bash++ function for the rest
// of the parse. It is what makes the ZERO-argument call `name()` recognizable:
// with arguments a call is unambiguous, but `name()` is also the prefix of a
// classic shell function definition, so only a name already known to be a
// Bash++ function may claim it — see [Parser.bashppParenForm]. Registering
// before the body is parsed is what lets a function recurse.
func (p *Parser) bashppRegisterFunc(name string) {
	if p.bashppFuncNames == nil {
		p.bashppFuncNames = make(map[string]bool)
	}
	p.bashppFuncNames[name] = true
}

// bashppPredeclaredFunc reports whether name is one of Go's predeclared
// functions that Bash++ implements. They are callable without a declaration,
// so they answer the zero-argument question [Parser.bashppRegisterFunc] answers
// for a declared name: `recover()` is a call, not the head of a shell function
// definition.
//
// The claim stays as narrow as the declared-name one. A body still rewinds the
// transaction — `recover() { …; }` is the shell function it has always been,
// because [bashppCallTerminator] admits only a statement end after the closing
// parenthesis — so what is claimed is exactly the shape bash rejects today.
func bashppPredeclaredFunc(name string) bool {
	switch name {
	case "append", "cap", "clear", "copy", "delete", "len", "make",
		"max", "min", "new", "panic", "print", "println", "recover":
		return true
	}
	return false
}

// bashppCallable reports whether a bare `name()` may be read as a Bash++ call:
// either a function declared earlier in this parse, or a predeclared one.
func (p *Parser) bashppCallable(name string) bool {
	return p.bashppFuncNames[name] || (p.lang.in(LangBashPP) && bashppPredeclaredFunc(name))
}

// bashppSig is a parsed Go-form signature: the parameter list, the optional
// results, and the positions of the parentheses that delimit them.
type bashppSig struct {
	params    []*BashPPField
	results   []*BashPPField
	lparen    Pos
	rparen    Pos
	resLparen Pos
	resRparen Pos
	nearMiss  bool
}

// bashppSignature parses `(params) results` with the parser sitting on the
// opening parenthesis. what names the construct for diagnostics, e.g.
// `func pick` or the bare `func` of a literal.
//
// The declaration and the literal share it deliberately: a literal's signature
// is the same grammar as a declaration's, and two copies would be two places
// for the variadic rules to drift apart.
func (p *Parser) bashppSignature(what string) bashppSig {
	sig := bashppSig{lparen: p.pos}
	p.next()
	sig.params, sig.nearMiss = p.bashppFieldList(sig.lparen, false)
	if p.tok != rightParen {
		p.followErr(sig.lparen, what+"(", rightParen)
	}
	sig.rparen = p.pos
	p.next()

	// Results are optional: none, a single bare type, or a parenthesised list.
	switch {
	case p.tok == leftParen:
		sig.resLparen = p.pos
		p.next()
		var nearMiss bool
		sig.results, nearMiss = p.bashppFieldList(sig.resLparen, true)
		sig.nearMiss = sig.nearMiss || nearMiss
		if p.tok != rightParen {
			p.followErr(sig.resLparen, what+"() (", rightParen)
		}
		sig.resRparen = p.pos
		p.next()
	case p.tok == rdrIn || p.tok == _LitWord && p.val == "chan":
		w := p.getWord()
		w = p.bashppChannelTypeWord(w)
		typExpr := bashppTypeExpr(w)
		if typExpr == nil {
			p.posErr(p.pos, "func result must be a channel type")
			break
		}
		text, _, _ := bashppScalarSource(w)
		sig.results = []*BashPPField{{FieldType: &Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: text}, FieldTypeExpr: typExpr}}
	case p.tok == _LitWord && !strings.HasPrefix(p.val, "{") && p.val != "}":
		typ := p.lit(p.pos, p.val)
		typExpr := bashppTypeExprFromLit(typ)
		if typExpr == nil {
			p.posErr(typ.Pos(), "func result must be a type name")
		}
		sig.results = []*BashPPField{{FieldType: typ, FieldTypeExpr: typExpr}}
		p.next()
	}
	return sig
}

// bashppFuncBody parses the braced body of a declaration or a literal, with
// the func depth raised so that a `return` inside it is the Go-form one.
func (p *Parser) bashppFuncBody(what string, after Pos, params []*BashPPField) *Block {
	// Concrete callable parameters enable zero-argument calls only in their body.
	// Restore the original lookup entries so a local parameter cannot claim a
	// later top-level shell spelling.
	saved := make(map[string]bool)
	for _, field := range params {
		if _, ok := field.FieldTypeExpr.(*BashPPFuncType); !ok {
			continue
		}
		for _, name := range field.Names {
			saved[name.Value] = p.bashppFuncNames[name.Value]
			p.bashppRegisterFunc(name.Value)
		}
	}
	defer func() {
		for name, exists := range saved {
			if exists {
				p.bashppFuncNames[name] = true
			} else {
				delete(p.bashppFuncNames, name)
			}
		}
	}()

	p.got(_Newl)
	if !(p.tok == _LitWord && (p.val == "{" || p.val == "{}")) {
		p.followErr(after, what+"()", noQuote("a { } body"))
	}
	// Branch targets do not cross a function boundary. A literal nested in a
	// typed loop starts with no enclosing control statements of its own; any
	// typed controls it declares below build a fresh stack.
	savedControls := p.bashppControls
	p.bashppControls = nil
	p.bashppFuncDepth++
	var body Stmt
	p.bashppBlock(&body)
	p.bashppFuncDepth--
	p.bashppControls = savedControls
	block, _ := body.Cmd.(*Block)
	return block
}

// bashppFuncLit parses a function literal with the parser sitting on the `(`
// that opens its parameter list, kw being the `func` already consumed.
//
// Like [Parser.bashppFuncForm] it commits forward rather than transactionally,
// and for the same reason: every site that reaches it — `f := func(`,
// `defer func(`, `return func(`, and the command-position `func(` with a
// parameter or a result — is a shape stock bash rejects outright, so there is
// no working script to protect and a malformed literal is a syntax error
// either way. What differs is only WHOSE diagnostic the user sees, and Go's is
// the more useful one here.
func (p *Parser) bashppFuncLit(kw *Lit) *BashPPFuncLit {
	lit := &BashPPFuncLit{Kw: kw}
	sig := p.bashppSignature("func")
	lit.Params, lit.Results = sig.params, sig.results
	lit.Lparen, lit.Rparen = sig.lparen, sig.rparen
	lit.ResLparen, lit.ResRparen = sig.resLparen, sig.resRparen
	lit.Body = p.bashppFuncBody("func", sig.rparen, sig.params)
	return lit
}

// bashppFieldList reads a comma-separated Go-form parameter or result list up to
// the closing parenthesis. result selects how bare identifiers resolve: as
// unnamed result types when true, and as untyped parameter names when false.
func (p *Parser) bashppFieldList(open Pos, result bool) ([]*BashPPField, bool) {
	type segment struct {
		lits   []*Lit
		def    *Word
		equals Pos
	}
	var segs []segment
	signatureTypes := make(map[*Lit]BashPPTypeExpr)
	var cur segment
	nearMiss := false
	for {
		p.got(_Newl)
		if p.tok == rightParen || p.tok == _EOF {
			break
		}
		w := p.getWord()
		channelSignature := p.tok == rdrIn && w == nil
		if head := bashppBareLit(w); head != nil && head.Value == "chan" {
			channelSignature = true
		}
		if channelSignature {
			w = p.bashppChannelTypeWord(w)
		}
		if w == nil {
			p.followErr(open, "(", noQuote("a parameter"))
			break
		}
		concreteSignature := false
		if head := bashppBareLit(w); head != nil && head.Value == "func" && p.tok == leftParen {
			w = p.bashppSignatureTypeWord(w)
			if w == nil {
				p.posErr(head.Pos(), "malformed concrete func type")
				break
			}
			concreteSignature = true
		}
		clean, comma := bashppTrimComma(w)
		lit := bashppBareLit(clean)
		if concreteSignature || channelSignature {
			text, _, ok := bashppScalarSource(clean)
			typ := bashppTypeExpr(clean)
			if !ok || typ == nil {
				p.posErr(clean.Pos(), "malformed concrete func type")
				break
			}
			lit = &Lit{ValuePos: clean.Pos(), ValueEnd: clean.End(), Value: text}
			signatureTypes[lit] = typ
		}
		if lit == nil {
			// A word that is not a single literal — `map[string]int` lexes as
			// two parts, say — is not a supported parameter spelling; report
			// it rather than dereferencing the missing literal below.
			p.posErr(w.Pos(), "func parameter must be a name or type")
			break
		}
		if lit.Value == "==" {
			nearMiss = true
		}
		if !result && lit.Value == "=" {
			if cur.def != nil || len(cur.lits) == 0 {
				p.posErr(lit.Pos(), "malformed func parameter list")
			}
			cur.equals = lit.Pos()
			def := p.getWord()
			if def == nil {
				p.posErr(lit.Pos(), "default parameter requires a value")
			}
			def, comma = bashppTrimComma(def)
			if !bashppCallArg(def) {
				p.posErr(def.Pos(), "default parameter must be a value")
			}
			cur.def = def
		} else {
			cur.lits = append(cur.lits, lit)
		}
		if comma {
			segs = append(segs, cur)
			cur = segment{}
		}
	}
	if len(cur.lits) > 0 || cur.def != nil {
		segs = append(segs, cur)
	}
	var fields []*BashPPField
	var err error
	var plain [][]*Lit
	flush := func() {
		if err != nil || len(plain) == 0 {
			return
		}
		var resolved []*BashPPField
		resolved, err = bashppResolveFields(plain, result)
		for _, field := range resolved {
			if typ := signatureTypes[field.FieldType]; typ != nil {
				field.FieldTypeExpr = typ
			}
		}
		fields = append(fields, resolved...)
		plain = nil
	}
	for _, seg := range segs {
		if seg.def == nil {
			plain = append(plain, seg.lits)
			continue
		}
		flush()
		if err != nil {
			break
		}
		if len(seg.lits) != 2 || !bashppIsIdent(seg.lits[0].Value) || bashppTypeExprFromLit(seg.lits[1]) == nil {
			err = errBashppFieldList
			break
		}
		fields = append(fields, &BashPPField{
			Names: seg.lits[:1], FieldType: seg.lits[1],
			FieldTypeExpr: bashppTypeExprFromLit(seg.lits[1]),
			Default:       seg.def, Equals: seg.equals,
		})
	}
	flush()
	switch {
	case err == nil:
	case err == errBashppFieldList:
		kind := "parameter"
		if result {
			kind = "result"
		}
		p.posErr(open, "malformed func %s list", kind)
	default:
		p.posErr(open, "%v", err)
	}
	return fields, nearMiss
}

func (p *Parser) bashppMarkTypeParamFields(fields []*BashPPField, params []*BashPPTypeParam) {
	bashppMarkTypeParamFields(fields, params)
}

func bashppMarkTypeParamFields(fields []*BashPPField, params []*BashPPTypeParam) {
	if len(params) == 0 {
		for _, field := range fields {
			if field.FieldTypeExpr == nil && field.FieldType != nil {
				field.FieldTypeExpr = bashppTypeExprFromLit(field.FieldType)
			}
		}
		return
	}
	names := make(map[string]bool)
	for _, param := range params {
		for _, name := range param.Names {
			names[name.Value] = true
		}
	}
	for _, field := range fields {
		if field.FieldTypeExpr == nil && field.FieldType != nil {
			field.FieldTypeExpr = bashppTypeExprFromLit(field.FieldType)
		}
		field.FieldTypeExpr = bashppTypeParamUses(field.FieldTypeExpr, names)
	}
}

func bashppTypeParamUses(typ BashPPTypeExpr, names map[string]bool) BashPPTypeExpr {
	switch x := typ.(type) {
	case *BashPPNamedType:
		if names[x.Name.Value] {
			return &BashPPTypeParamType{Name: x.Name}
		}
		if len(x.TypeArgs) > 0 {
			cp := *x
			cp.TypeArgs = append([]*BashPPTypeArg(nil), x.TypeArgs...)
			for i, arg := range cp.TypeArgs {
				ac := *arg
				ac.ArgType = bashppTypeParamUses(ac.ArgType, names)
				cp.TypeArgs[i] = &ac
			}
			return &cp
		}
	case *BashPPChanType:
		cp := *x
		cp.Element = bashppTypeParamUses(cp.Element, names)
		return &cp
	case *BashPPCollectionType:
		cp := *x
		cp.Key = bashppTypeParamUses(cp.Key, names)
		cp.Element = bashppTypeParamUses(cp.Element, names)
		return &cp
	case *BashPPPointerType:
		cp := *x
		cp.Element = bashppTypeParamUses(cp.Element, names)
		return &cp
	case *BashPPStructType:
		cp := *x
		cp.Fields = append([]*BashPPField(nil), x.Fields...)
		for i, field := range cp.Fields {
			fc := *field
			fc.FieldTypeExpr = bashppTypeParamUses(fc.FieldTypeExpr, names)
			cp.Fields[i] = &fc
		}
		return &cp
	case *BashPPInterfaceType:
		cp := *x
		cp.Elems = append([]*BashPPInterfaceElem(nil), x.Elems...)
		for i, elem := range cp.Elems {
			ec := *elem
			ec.Embedded = bashppTypeParamUses(ec.Embedded, names)
			cp.Elems[i] = &ec
		}
		return &cp
	case *BashPPUnionType:
		cp := *x
		cp.Terms = append([]BashPPTypeExpr(nil), x.Terms...)
		for i, term := range cp.Terms {
			cp.Terms[i] = bashppTypeParamUses(term, names)
		}
		return &cp
	case *BashPPApproxType:
		cp := *x
		cp.Term = bashppTypeParamUses(cp.Term, names)
		return &cp
	}
	return typ
}

func bashppTypeParamList(items []*Lit) ([]*BashPPTypeParam, bool) {
	if len(items) == 0 {
		return nil, false
	}
	var segs [][]*Lit
	var cur []*Lit
	for _, item := range items {
		parts := strings.Split(item.Value, ",")
		for i, part := range parts {
			if part != "" {
				start := strings.Index(item.Value, part)
				pos := posAddCol(item.Pos(), start)
				cur = append(cur, &Lit{ValuePos: pos, ValueEnd: posAddCol(pos, len(part)), Value: part})
			}
			if i < len(parts)-1 {
				if len(cur) == 0 {
					return nil, false
				}
				segs = append(segs, cur)
				cur = nil
			}
		}
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	var out []*BashPPTypeParam
	var pending []*Lit
	for _, seg := range segs {
		switch len(seg) {
		case 1:
			pending = append(pending, seg[0])
		case 2:
			for _, name := range append(pending, seg[0]) {
				if !bashppIsIdent(name.Value) {
					return nil, false
				}
			}
			constraintLit := seg[1]
			if strings.Contains(constraintLit.Value, `\|`) {
				cp := *constraintLit
				cp.Value = strings.ReplaceAll(cp.Value, `\|`, "|")
				constraintLit = &cp
			}
			constraint := bashppTypeExprFromLit(constraintLit)
			if constraint == nil {
				return nil, false
			}
			out = append(out, &BashPPTypeParam{Names: append(pending, seg[0]), Constraint: constraint})
			pending = nil
		default:
			for _, name := range append(pending, seg[0]) {
				if !bashppIsIdent(name.Value) {
					return nil, false
				}
			}
			var b strings.Builder
			for i, lit := range seg[1:] {
				if i > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(strings.ReplaceAll(lit.Value, `\|`, "|"))
			}
			start := seg[1].Pos()
			constraintLit := &Lit{ValuePos: start, ValueEnd: seg[len(seg)-1].End(), Value: b.String()}
			constraint := bashppTypeExprFromLit(constraintLit)
			if constraint == nil {
				return nil, false
			}
			out = append(out, &BashPPTypeParam{Names: append(pending, seg[0]), Constraint: constraint})
			pending = nil
		}
	}
	if len(pending) != 0 {
		return nil, false
	}
	return out, true
}

// errBashppFieldList is the generic "this is not a signature we spell" verdict,
// reported as a malformed list. A field-list rule with a Go diagnostic of its
// own — the only-final rule for `...` — returns that message instead, because
// "malformed parameter list" would send a reader looking for a typo that is
// not there.
var errBashppFieldList = errors.New("malformed func field list")

// errBashppEllipsisFinal is Go's own wording for a variadic group that is not
// last, which is the mistake a reader is most likely to make once `...` exists.
var errBashppEllipsisFinal = errors.New("can only use ... with final parameter")

// bashppResolveFields turns the comma-separated word groups of a signature into
// [BashPPField]s, applying Go's rule that a trailing type distributes back over
// the names that precede it, plus the Bash++ extension that a run of bare
// parameter names with no type at all is an untyped group.
func bashppResolveFields(segs [][]*Lit, result bool) ([]*BashPPField, error) {
	var fields []*BashPPField
	var pending []*Lit
	sawType := false
	sawVariadic := false
	for _, seg := range segs {
		// A `...T` group ends the list. Go allows the variadic parameter only
		// last, and allows it only ONE name — `func f(a, b ...int)` shares the
		// type across both names and is rejected by the gc front end with the
		// message reused here — so both rules are checked where the group is
		// built rather than left to a later pass that would have to
		// reconstruct which group carried the dots.
		if last := seg[len(seg)-1]; strings.HasPrefix(last.Value, "...") {
			if result || sawVariadic || len(seg) > 2 || len(pending) > 0 {
				return nil, errBashppEllipsisFinal
			}
			elem, ok := bashppEllipsisElem(last)
			if !ok {
				return nil, errBashppFieldList
			}
			field := &BashPPField{FieldType: elem, Ellipsis: last.Pos()}
			if len(seg) == 2 {
				if !bashppIsIdent(seg[0].Value) {
					return nil, errBashppFieldList
				}
				field.Names = []*Lit{seg[0]}
			}
			fields = append(fields, field)
			sawType, sawVariadic = true, true
			continue
		}
		if sawVariadic {
			return nil, errBashppEllipsisFinal
		}
		switch len(seg) {
		case 1:
			pending = append(pending, seg[0])
		case 2:
			name, typ := seg[0], seg[1]
			typExpr := bashppTypeExprFromLit(typ)
			if typExpr == nil {
				return nil, errBashppFieldList
			}
			names := append(pending, name)
			for _, n := range names {
				if !bashppIsIdent(n.Value) {
					return nil, errBashppFieldList
				}
			}
			fields = append(fields, &BashPPField{Names: names, FieldType: typ, FieldTypeExpr: typExpr})
			pending = nil
			sawType = true
		default:
			return nil, errBashppFieldList
		}
	}
	if len(pending) == 0 {
		return fields, nil
	}
	// Trailing bare identifiers. After a typed group they are a mixed
	// named/unnamed list, which Go rejects; with no type anywhere they are
	// either unnamed result types or untyped parameter names.
	if sawType {
		return nil, errBashppFieldList
	}
	if result {
		for _, t := range pending {
			typExpr := bashppTypeExprFromLit(t)
			if typExpr == nil {
				return nil, errBashppFieldList
			}
			fields = append(fields, &BashPPField{FieldType: t, FieldTypeExpr: typExpr})
		}
		return fields, nil
	}
	for _, n := range pending {
		if !bashppIsIdent(n.Value) {
			return nil, errBashppFieldList
		}
	}
	return append(fields, &BashPPField{Names: pending}), nil
}

// bashppEllipsisElem splits a `...T` word into the element type T, positioned
// where T was written so the printer and every diagnostic point at the type
// rather than at the dots.
func bashppEllipsisElem(lit *Lit) (*Lit, bool) {
	name := strings.TrimPrefix(lit.Value, "...")
	if bashppTypeExprFromLit(&Lit{ValuePos: posAddCol(lit.ValuePos, 3), ValueEnd: lit.ValueEnd, Value: name}) == nil {
		return nil, false
	}
	pos := posAddCol(lit.ValuePos, 3)
	return &Lit{ValuePos: pos, ValueEnd: posAddCol(pos, len(name)), Value: name}, true
}

// bashppTypeName reports whether s is a supported type spelling: a bare
// identifier, dotted selector (`time.Duration`), or the pointer to a named
// type needed by P3-C. Other composite types remain a later tranche.
func bashppTypeName(s string) bool {
	// `func` is the one reserved word admitted as a type spelling, and it is
	// the WHOLE spelling: P3-B gives a function value the bare type `func`
	// rather than Go's full `func(int) error`, whose parentheses and commas
	// would have to survive the shell's word splitting inside a signature that
	// is itself parenthesised. A parameter or result typed `func` holds a
	// closure; the argument and result types it accepts are unchecked, which
	// is the same latitude every other type spelling gets in this phase.
	if s == "func" {
		return true
	}
	if strings.HasPrefix(s, "*") {
		return bashppIsIdent(strings.TrimPrefix(s, "*"))
	}
	if !bashppSelector(s) {
		return false
	}
	head, _, _ := strings.Cut(s, ".")
	return head != "" && !isGoReservedWord(head)
}

// bashppDeferForm parses `defer f(x)` once the call parser has collected
// `defer f` and stopped at `(`. The call itself is recognized by the shared
// paren-form machinery, so an unsupported argument rewinds to the shell exactly
// as a bare call would, keeping `defer cleanup` and other Class E shapes shell.
//
// `defer func(…) { … }()` — a deferred closure — is the same site with a
// literal in callee position. It cannot rewind, because the literal commits
// forward like every other one; that costs nothing, since the parenthesis
// after `defer func` is a bash syntax error whatever follows it.
func (p *Parser) bashppDeferForm(ce *CallExpr) Command {
	if ce == nil || len(ce.Assigns) != 0 || len(ce.Args) != 2 || p.tok != leftParen || p.spaced {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	name := bashppBareLit(ce.Args[1])
	if kw == nil || kw.Value != "defer" || name == nil {
		return nil
	}
	// The peek byte is part of the probe because `defer func()` and
	// `defer func(n int)` differ only past the parenthesis, and the table
	// answers about the literal from the byte that follows it.
	if RecognizeStartSite(kw.Value+" "+name.Value+"("+p.bashppPeekByte()).Site != StartDefer {
		return nil
	}
	if name.Value == "func" {
		call := p.bashppLitCall(p.bashppFuncLit(name))
		if call == nil {
			return nil
		}
		return &BashPPDefer{Kw: kw, Call: call}
	}
	if !bashppSelector(name.Value) {
		return nil
	}
	cmd := p.bashppParenForm(&CallExpr{Args: []*Word{ce.Args[1]}})
	call, ok := cmd.(*BashPPCall)
	if !ok {
		return nil
	}
	return &BashPPDefer{Kw: kw, Call: call}
}

// bashppFuncLitForm claims the sites at which a function literal may stand,
// with the parser sitting on the `(` that opens the literal's parameter list
// and ce holding the words already collected:
//
//	func(n int) { … }(1)            an immediately invoked literal
//	greet := func(who string) { … } a literal bound to a name
//	n := func() int { … }()         a literal invoked into a binding
//	return func(n int) int { … }    a literal escaping its factory
//
// `defer func() { … }()` is the fifth, and belongs to [Parser.bashppDeferForm]
// because `defer` reaches the dispatch first.
//
// It returns nil for every other shape, including the bare `func(` at a
// command position with an empty parameter list, which stays the bash function
// definition it is today — see [recognizeFuncLit].
func (p *Parser) bashppFuncLitForm(ce *CallExpr) Command {
	kw := bashppBareLit(ce.Args[len(ce.Args)-1])
	if kw == nil || kw.Value != "func" {
		return nil
	}
	switch {
	case len(ce.Args) == 1:
		// A command position. Only the recognizer decides whether the region
		// opens here, because this is the one literal site that competes with
		// a shape bash accepts.
		if RecognizeStartSite("func("+p.bashppPeekByte()).Site != StartFuncLit {
			return nil
		}
		call := p.bashppLitCall(p.bashppFuncLit(kw))
		if call == nil {
			return nil
		}
		return call
	case len(ce.Args) == 2 && bashppLitValue(ce.Args[0]) == "return":
		// `return func(…) { … }`, which is how a closure escapes the function
		// that built it. It is claimed only inside a committed func body: a
		// `return` outside one is the shell builtin, which is Class E, and
		// nothing about it may change.
		if p.bashppFuncDepth == 0 {
			return nil
		}
		ret := bashppBareLit(ce.Args[0])
		return &BashPPReturn{Kw: ret, FuncLit: p.bashppFuncLit(kw)}
	case len(ce.Args) >= 3 && bashppLitValue(ce.Args[len(ce.Args)-2]) == ":=":
		lhs, ok := bashppShortLHS(ce.Args[:len(ce.Args)-2])
		if !ok {
			return nil
		}
		opPos := ce.Args[len(ce.Args)-2].Pos()
		lit := p.bashppFuncLit(kw)
		if p.tok == leftParen && !p.spaced {
			// `n := func() int { … }()` binds the RESULT of the call, so the
			// node carries the invocation and not the function.
			call := p.bashppLitCall(lit)
			if call == nil {
				return nil
			}
			return &BashPPShortDecl{Lhs: lhs, Class: ClassR, OpPos: opPos, GoRegion: true, Call: call}
		}
		// A name bound to a literal is callable with no arguments from here
		// on, which is the whole point of binding it; see
		// [Parser.bashppRegisterFunc] for why that has to be recorded.
		for _, name := range lhs {
			p.bashppRegisterFunc(name.Value)
		}
		return &BashPPShortDecl{Lhs: lhs, Class: ClassR, OpPos: opPos, GoRegion: true, FuncLit: lit}
	}
	return nil
}

// bashppLitValue is the bare literal text of w, or "" when w is not one.
func bashppLitValue(w *Word) string {
	if lit := bashppBareLit(w); lit != nil {
		return lit.Value
	}
	return ""
}

// bashppPeekByte is the one byte past the parser's current token, as a string,
// or "" at end of input. It exists so a recognizer probe can be assembled from
// the same lookahead the streaming lexer already holds, without reading ahead.
func (p *Parser) bashppPeekByte() string {
	if p.r >= utf8.RuneSelf {
		return ""
	}
	return string(p.r)
}

// bashppLitCall parses the `(args)` that immediately invokes a literal.
//
// A literal at these sites must be called: Go has no bare-literal statement
// either, and the sites that do NOT require an invocation — `greet := func…`,
// `return func…` — never reach here.
func (p *Parser) bashppLitCall(lit *BashPPFuncLit) *BashPPCall {
	if p.tok != leftParen {
		p.posErr(lit.Pos(), "func literal must be called or bound to a name")
		return nil
	}
	lparen := p.pos
	p.next()
	args, argNames, ellipsis, ok := p.bashppCallArgs()
	if !ok || p.tok != rightParen {
		p.posErr(lparen, "malformed func literal argument list")
		return nil
	}
	rparen := p.pos
	p.next()
	return &BashPPCall{
		FuncLit: lit, Args: args, ArgNames: argNames, Ellipsis: ellipsis,
		Lparen: lparen, Rparen: rparen,
	}
}

// bashppReturn reclassifies a completed `return …` command as a Go-form return,
// but only inside a Bash++ func body. Outside one it returns nil so the command
// stays the ordinary shell builtin, which is Class E and must keep its meaning.
func (p *Parser) bashppReturn(ce *CallExpr, redirs []*Redirect) *BashPPReturn {
	if p.bashppFuncDepth == 0 || ce == nil || len(ce.Assigns) != 0 || len(redirs) != 0 || len(ce.Args) == 0 {
		return nil
	}
	kw := bashppBareLit(ce.Args[0])
	if kw == nil || kw.Value != "return" {
		return nil
	}
	if len(ce.Args) == 1 {
		return &BashPPReturn{Kw: kw}
	}
	vals := bashppReturnValues(ce.Args[1:])
	if vals == nil {
		// A comma placement the tuple grammar does not spell — leave it to the
		// shell `return` builtin rather than guess at the intent.
		return nil
	}
	return &BashPPReturn{Kw: kw, Results: vals}
}

// bashppReturnValues splits a return operand list into one word per value.
// Inside a committed func body the compatibility grammar no longer applies —
// bash never reaches this code — so any word may be returned; only the tuple's
// comma placement is checked, so `return a, b` yields two values while a stray
// comma falls back to the shell builtin.
func bashppReturnValues(words []*Word) []*Word {
	out := make([]*Word, 0, len(words))
	for i, w := range words {
		clean, comma := bashppTrimComma(w)
		if comma != (i < len(words)-1) {
			return nil
		}
		out = append(out, clean)
	}
	return out
}
