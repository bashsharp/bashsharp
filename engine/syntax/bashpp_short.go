// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"bytes"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"io"
	"strings"
)

// bashppShortDecl recognizes the Class-E half of := after the ordinary shell
// parser has completed the command. No speculative input is involved here.
func bashppShortDecl(ce *CallExpr, redirs []*Redirect, goRegion bool) *BashPPShortDecl {
	if ce == nil || len(ce.Assigns) != 0 || len(redirs) != 0 {
		return nil
	}
	op := -1
	for i, w := range ce.Args {
		if l := bashppBareLit(w); l != nil && l.Value == ":=" {
			if op >= 0 {
				return nil
			}
			op = i
		}
	}
	if op < 1 || op+1 >= len(ce.Args) {
		return nil
	}
	lhs, ok := bashppShortLHS(ce.Args[:op])
	if !ok {
		return nil
	}
	rhs, ok := bashppShortValues(ce.Args[op+1:], goRegion)
	if !ok || (len(lhs) > 1 && len(lhs) != len(rhs)) {
		return nil
	}
	m := RecognizeStartSite(shortDeclHead(lhs) + " := " + bashppWordText(rhs[0]))
	if m.Site != StartShortDecl || m.Class != ClassE {
		return nil
	}
	d := &BashPPShortDecl{Lhs: lhs, Rhs: rhs, Class: ClassE, OpPos: ce.Args[op].Pos(), GoRegion: goRegion}
	if len(rhs) == 1 {
		// Collections and indexed reads are meaningful at Class-E sites which
		// were already claimed as short declarations. Ordinary scalar words
		// retain shell meaning unless an enclosing Go region owns them.
		if expr := bashppCompositeExpr(rhs[0]); expr != nil {
			d.Expr, d.Rhs = expr, nil
		} else if expr := bashppIndexExpr(rhs[0]); expr != nil {
			d.Expr, d.Rhs = expr, nil
		} else if goRegion {
			expr := bashppScalarExpr(rhs[0])
			if expr != nil {
				d.Expr, d.Rhs = expr, nil
			}
		}
	}
	if len(rhs) == 1 {
		if lit := bashppBareLit(rhs[0]); lit != nil && strings.Contains(lit.Value, ".") && bashppSelector(lit.Value) {
			d.MethodValue = bashppSelectorLits(lit)
		}
	}
	return d
}

// bashppScalarExpr translates the parser's bounded Go expression probe into
// our own AST immediately. The interpreter must never reparse a source word.
func bashppScalarExpr(w *Word) BashPPExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedScalarAST(expr) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p gotoken.Pos, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

func bashppAssignmentTargetExpr(w *Word) BashPPExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedAddressAST(expr) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p gotoken.Pos, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

func bashppConvertExpr(expr goast.Expr, source string, pos func(gotoken.Pos) Pos, lit func(gotoken.Pos, gotoken.Pos, string) *Lit) BashPPExpr {
	convertType := func(e goast.Expr) BashPPTypeExpr { return bashppConvertType(e, pos, lit) }

	var convert func(goast.Expr) BashPPExpr
	convert = func(e goast.Expr) BashPPExpr {
		switch x := e.(type) {
		case *goast.BasicLit:
			return &BashPPBasicLit{Value: lit(x.Pos(), x.End(), x.Value), Kind: x.Kind.String()}
		case *goast.Ident:
			return &BashPPIdent{Name: lit(x.Pos(), x.End(), x.Name)}
		case *goast.ParenExpr:
			return &BashPPParenExpr{Lparen: pos(x.Pos()), X: convert(x.X), Rparen: pos(x.End() - 1)}
		case *goast.UnaryExpr:
			switch x.Op {
			case gotoken.AND:
				return &BashPPAddressExpr{Amp: pos(x.OpPos), X: convert(x.X)}
			case gotoken.MUL:
				return &BashPPDerefExpr{Star: pos(x.OpPos), X: convert(x.X)}
			}
			return &BashPPUnaryExpr{Op: lit(x.OpPos, x.OpPos+gotoken.Pos(len(x.Op.String())), x.Op.String()), X: convert(x.X)}
		case *goast.StarExpr:
			return &BashPPDerefExpr{Star: pos(x.Star), X: convert(x.X)}
		case *goast.BinaryExpr:
			return &BashPPBinaryExpr{X: convert(x.X), Op: lit(x.OpPos, x.OpPos+gotoken.Pos(len(x.Op.String())), x.Op.String()), Y: convert(x.Y)}
		case *goast.CallExpr:
			id := x.Fun.(*goast.Ident)
			if id.Name != "new" && !bashppScalarConversionType(id.Name) {
				call := &BashPPCall{Fun: []*Lit{lit(id.Pos(), id.End(), id.Name)}, Lparen: pos(x.Lparen), Rparen: pos(x.Rparen)}
				for _, arg := range x.Args {
					call.Args = append(call.Args, &Word{Parts: []WordPart{lit(arg.Pos(), arg.End(), source[int(arg.Pos())-1:int(arg.End())-1])}})
					if id.Name != "len" && id.Name != "cap" {
						call.ArgExprs = append(call.ArgExprs, convert(arg))
					}
				}
				return call
			}
			if id.Name == "new" {
				return &BashPPNewExpr{New: lit(id.Pos(), id.End(), id.Name), Lparen: pos(id.End()), AllocType: convertType(x.Args[0]), Rparen: pos(x.End() - 1)}
			}
			return &BashPPConvertExpr{ConvType: lit(id.Pos(), id.End(), id.Name), Lparen: pos(id.End()), X: convert(x.Args[0]), Rparen: pos(x.End() - 1)}
		case *goast.IndexExpr:
			return &BashPPIndexExpr{X: convert(x.X), Lbrack: pos(x.Lbrack), Index: convert(x.Index), Rbrack: pos(x.End() - 1)}
		case *goast.SliceExpr:
			colons := bashppSliceColonOffsets(source, x.Lbrack, x.End())
			out := &BashPPSliceExpr{X: convert(x.X), Lbrack: pos(x.Lbrack), Rbrack: pos(x.End() - 1)}
			if len(colons) > 0 {
				out.Colon = pos(gotoken.Pos(colons[0] + 1))
			}
			if len(colons) > 1 {
				out.SecondColon = pos(gotoken.Pos(colons[1] + 1))
			}
			if x.Low != nil {
				out.Low = convert(x.Low)
			}
			if x.High != nil {
				out.High = convert(x.High)
			}
			if x.Max != nil {
				out.Max = convert(x.Max)
			}
			return out
		case *goast.SelectorExpr:
			return &BashPPSelectorExpr{X: convert(x.X), Dot: pos(x.Sel.Pos() - 1), Sel: lit(x.Sel.Pos(), x.Sel.End(), x.Sel.Name)}
		case *goast.TypeAssertExpr:
			out := &BashPPTypeAssertExpr{X: convert(x.X), Dot: pos(x.Lparen - 1), Lparen: pos(x.Lparen), Rparen: pos(x.Rparen)}
			if x.Type != nil {
				out.Assert = convertType(x.Type)
			}
			return out
		case *goast.CompositeLit:
			out := &BashPPCompositeLit{LitType: convertType(x.Type), Lbrace: pos(x.Lbrace), Rbrace: pos(x.Rbrace)}
			for _, raw := range x.Elts {
				elem := &BashPPCompositeElem{}
				if kv, ok := raw.(*goast.KeyValueExpr); ok {
					elem.Key, elem.Colon, elem.Value = convert(kv.Key), pos(kv.Colon), convert(kv.Value)
				} else {
					elem.Value = convert(raw)
				}
				out.Elems = append(out.Elems, elem)
			}
			return out
		}
		return nil
	}
	return convert(expr)
}

func bashppConvertType(e goast.Expr, pos func(gotoken.Pos) Pos, lit func(gotoken.Pos, gotoken.Pos, string) *Lit) BashPPTypeExpr {
	switch x := e.(type) {
	case *goast.ChanType:
		direction := ""
		if x.Dir == goast.SEND {
			direction = "send"
		}
		if x.Dir == goast.RECV {
			direction = "recv"
		}
		chanPos := pos(x.Begin)
		if direction == "recv" {
			chanPos = posAddCol(chanPos, 2)
		}
		out := &BashPPChanType{Chan: chanPos, Direction: direction, Element: bashppConvertType(x.Value, pos, lit), Elem: lit(x.Value.Pos(), x.Value.End(), bashppGoTypeText(x.Value))}
		if x.Arrow.IsValid() {
			out.Arrow = pos(x.Arrow)
		}
		return out
	case *goast.FuncType:
		out := &BashPPFuncType{Func: pos(x.Func), Lparen: pos(x.Params.Opening), Rparen: pos(x.Params.Closing)}
		fields := func(list *goast.FieldList) []*BashPPField {
			if list == nil {
				return nil
			}
			var result []*BashPPField
			for _, f := range list.List {
				field := &BashPPField{FieldType: lit(f.Type.Pos(), f.Type.End(), bashppGoTypeText(f.Type)), FieldTypeExpr: bashppConvertType(f.Type, pos, lit)}
				for _, n := range f.Names {
					field.Names = append(field.Names, lit(n.Pos(), n.End(), n.Name))
				}
				result = append(result, field)
			}
			return result
		}
		out.Params, out.Results = fields(x.Params), fields(x.Results)
		if x.Results != nil && x.Results.Opening.IsValid() {
			out.ResLparen, out.ResRparen = pos(x.Results.Opening), pos(x.Results.Closing)
		}
		return out
	case *goast.Ident:
		return &BashPPNamedType{Name: lit(x.Pos(), x.End(), x.Name)}
	case *goast.SelectorExpr:
		if _, ok := x.X.(*goast.Ident); !ok {
			return nil
		}
		return &BashPPNamedType{Name: lit(x.Pos(), x.End(), bashppGoTypeText(x))}
	case *goast.IndexExpr:
		name, ok := x.X.(*goast.Ident)
		if !ok {
			if _, selector := x.X.(*goast.SelectorExpr); !selector {
				return nil
			}
			return &BashPPNamedType{Name: lit(x.X.Pos(), x.X.End(), bashppGoTypeText(x.X)), TypeArgs: []*BashPPTypeArg{{ArgType: bashppConvertType(x.Index, pos, lit)}}}
		}
		return &BashPPNamedType{Name: lit(name.Pos(), name.End(), name.Name), TypeArgs: []*BashPPTypeArg{{ArgType: bashppConvertType(x.Index, pos, lit)}}}
	case *goast.IndexListExpr:
		name, ok := x.X.(*goast.Ident)
		if !ok {
			if _, selector := x.X.(*goast.SelectorExpr); !selector {
				return nil
			}
			out := &BashPPNamedType{Name: lit(x.X.Pos(), x.X.End(), bashppGoTypeText(x.X))}
			for _, index := range x.Indices {
				out.TypeArgs = append(out.TypeArgs, &BashPPTypeArg{ArgType: bashppConvertType(index, pos, lit)})
			}
			return out
		}
		out := &BashPPNamedType{Name: lit(name.Pos(), name.End(), name.Name)}
		for _, index := range x.Indices {
			out.TypeArgs = append(out.TypeArgs, &BashPPTypeArg{ArgType: bashppConvertType(index, pos, lit)})
		}
		return out
	case *goast.BinaryExpr:
		if x.Op != gotoken.OR {
			return nil
		}
		var terms []BashPPTypeExpr
		var bars []*Lit
		var flatten func(goast.Expr) bool
		flatten = func(e goast.Expr) bool {
			if b, ok := e.(*goast.BinaryExpr); ok && b.Op == gotoken.OR {
				if !flatten(b.X) {
					return false
				}
				bars = append(bars, lit(b.OpPos, b.OpPos+1, "|"))
				return flatten(b.Y)
			}
			term := bashppConvertType(e, pos, lit)
			if term == nil {
				return false
			}
			terms = append(terms, term)
			return true
		}
		if !flatten(x) || len(terms) < 2 {
			return nil
		}
		return &BashPPUnionType{Terms: terms, Bars: bars}
	case *goast.UnaryExpr:
		if x.Op != gotoken.TILDE {
			return nil
		}
		term := bashppConvertType(x.X, pos, lit)
		if term == nil {
			return nil
		}
		return &BashPPApproxType{Tilde: pos(x.OpPos), Term: term}
	case *goast.ArrayType:
		kind := "slice"
		var length *Lit
		if x.Len != nil {
			kind = "array"
			switch n := x.Len.(type) {
			case *goast.BasicLit:
				length = lit(n.Pos(), n.End(), n.Value)
			case *goast.Ellipsis:
				kind = "inferred-array"
				length = lit(n.Pos(), n.End(), "...")
			default:
				length = lit(n.Pos(), n.End(), bashppGoExprText(n))
			}
		}
		return &BashPPCollectionType{Kind: kind, Start: pos(x.Pos()), Lbrack: pos(x.Lbrack), Length: length, Rbrack: pos(x.Elt.Pos() - 1), Element: bashppConvertType(x.Elt, pos, lit)}
	case *goast.MapType:
		return &BashPPCollectionType{Kind: "map", Start: pos(x.Pos()), Lbrack: pos(x.Map + 3), Rbrack: pos(x.Value.Pos() - 1), Key: bashppConvertType(x.Key, pos, lit), Element: bashppConvertType(x.Value, pos, lit)}
	case *goast.StructType:
		out := &BashPPStructType{Struct: lit(x.Struct, x.Struct+6, "struct"), Lbrace: pos(x.Fields.Opening), Rbrace: pos(x.Fields.Closing)}
		for _, field := range x.Fields.List {
			entry := &BashPPField{FieldTypeExpr: bashppConvertType(field.Type, pos, lit), Embedded: len(field.Names) == 0}
			entry.FieldType = lit(field.Type.Pos(), field.Type.End(), bashppGoTypeText(field.Type))
			for _, name := range field.Names {
				entry.Names = append(entry.Names, lit(name.Pos(), name.End(), name.Name))
			}
			out.Fields = append(out.Fields, entry)
		}
		return out
	case *goast.InterfaceType:
		out := &BashPPInterfaceType{Interface: lit(x.Interface, x.Interface+9, "interface"), Lbrace: pos(x.Methods.Opening), Rbrace: pos(x.Methods.Closing)}
		for _, field := range x.Methods.List {
			ft, ok := field.Type.(*goast.FuncType)
			if !ok {
				if len(field.Names) != 0 {
					return nil
				}
				embedded := bashppConvertType(field.Type, pos, lit)
				if embedded == nil {
					return nil
				}
				out.Elems = append(out.Elems, &BashPPInterfaceElem{Embedded: embedded})
				continue
			}
			if len(field.Names) != 1 {
				return nil
			}
			spec := &BashPPMethodSpec{Name: lit(field.Names[0].Pos(), field.Names[0].End(), field.Names[0].Name),
				Lparen: pos(ft.Params.Opening), Rparen: pos(ft.Params.Closing)}
			for _, param := range ft.Params.List {
				entry := &BashPPField{FieldTypeExpr: bashppConvertType(param.Type, pos, lit), FieldType: lit(param.Type.Pos(), param.Type.End(), bashppGoTypeText(param.Type))}
				for _, name := range param.Names {
					entry.Names = append(entry.Names, lit(name.Pos(), name.End(), name.Name))
				}
				spec.Params = append(spec.Params, entry)
			}
			if ft.Results != nil {
				spec.ResLparen, spec.ResRparen = pos(ft.Results.Opening), pos(ft.Results.Closing)
				for _, result := range ft.Results.List {
					entry := &BashPPField{FieldTypeExpr: bashppConvertType(result.Type, pos, lit), FieldType: lit(result.Type.Pos(), result.Type.End(), bashppGoTypeText(result.Type))}
					for _, name := range result.Names {
						entry.Names = append(entry.Names, lit(name.Pos(), name.End(), name.Name))
					}
					spec.Results = append(spec.Results, entry)
				}
			}
			out.Elems = append(out.Elems, &BashPPInterfaceElem{Method: spec})
			out.Methods = append(out.Methods, spec)
		}
		return out
	case *goast.StarExpr:
		return &BashPPPointerType{Star: pos(x.Star), Element: bashppConvertType(x.X, pos, lit)}
	}
	return nil
}

func bashppTypeExpr(w *Word) BashPPTypeExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedTypeAST(expr) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertType(expr, pos, lit)
}

// BashPPTypeExprFromText parses a type spelled as text — the spelling a
// scalar carries after a conversion such as `IteratorFunc[int](it)` — into
// its type tree, or nil when the text is not a supported type.
func BashPPTypeExprFromText(text string) BashPPTypeExpr {
	return bashppTypeExprFromLit(&Lit{Value: text})
}

func bashppTypeExprFromLit(src *Lit) BashPPTypeExpr {
	if src == nil {
		return nil
	}
	if src.Value == "func" {
		return &BashPPNamedType{Name: src}
	}
	expr, err := goparser.ParseExpr(src.Value)
	if err != nil || !bashppSupportedTypeAST(expr) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return posAddCol(src.Pos(), int(p)-1) }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertType(expr, pos, lit)
}

func bashppCallTypeArgs(name *Lit) (*Lit, []*BashPPTypeArg, bool) {
	open := strings.IndexByte(name.Value, '[')
	if open < 0 {
		return name, nil, true
	}
	if !strings.HasSuffix(name.Value, "]") {
		return nil, nil, false
	}
	baseText := name.Value[:open]
	if !bashppSelector(baseText) {
		return nil, nil, false
	}
	base := &Lit{ValuePos: name.Pos(), ValueEnd: posAddCol(name.Pos(), len(baseText)), Value: baseText}
	body := name.Value[open+1 : len(name.Value)-1]
	if body == "" {
		return nil, nil, false
	}
	start := posAddCol(name.Pos(), open+1)
	var args []*BashPPTypeArg
	depth, partStart := 0, 0
	for i, r := range body {
		switch r {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return nil, nil, false
			}
			depth--
		case ',':
			if depth == 0 {
				arg, ok := bashppTypeArgFromText(body[partStart:i], posAddCol(start, partStart))
				if !ok {
					return nil, nil, false
				}
				args = append(args, arg)
				partStart = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, nil, false
	}
	arg, ok := bashppTypeArgFromText(body[partStart:], posAddCol(start, partStart))
	if !ok {
		return nil, nil, false
	}
	args = append(args, arg)
	return base, args, true
}

func bashppTypeArgFromText(text string, pos Pos) (*BashPPTypeArg, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	typ := bashppTypeExprFromLit(&Lit{ValuePos: pos, ValueEnd: posAddCol(pos, len(text)), Value: text})
	if typ == nil {
		return nil, false
	}
	return &BashPPTypeArg{ArgType: typ}, true
}

func bashppTypeText(typ BashPPTypeExpr) string {
	switch x := typ.(type) {
	case *BashPPChanType:
		prefix := "chan "
		if x.Direction == "send" {
			prefix = "chan<- "
		}
		if x.Direction == "recv" {
			prefix = "<-chan "
		}
		if x.Element != nil {
			return prefix + bashppTypeText(x.Element)
		}
		if x.Elem != nil {
			return prefix + x.Elem.Value
		}
		return prefix
	case *BashPPFuncType:
		text := func(fields []*BashPPField) string {
			var parts []string
			for _, f := range fields {
				var names []string
				for _, n := range f.Names {
					names = append(names, n.Value)
				}
				part := bashppTypeText(f.FieldTypeExpr)
				if len(names) > 0 {
					part = strings.Join(names, ", ") + " " + part
				}
				parts = append(parts, part)
			}
			return strings.Join(parts, ", ")
		}
		result := "func(" + text(x.Params) + ")"
		if len(x.Results) > 0 {
			if x.ResLparen.IsValid() {
				result += " (" + text(x.Results) + ")"
			} else {
				result += " " + text(x.Results)
			}
		}
		return result
	case *BashPPNamedType:
		if len(x.TypeArgs) == 0 {
			return x.Name.Value
		}
		var args []string
		for _, arg := range x.TypeArgs {
			args = append(args, bashppTypeText(arg.ArgType))
		}
		return x.Name.Value + "[" + strings.Join(args, ", ") + "]"
	case *BashPPTypeParamType:
		return x.Name.Value
	case *BashPPUnionType:
		var terms []string
		for _, term := range x.Terms {
			terms = append(terms, bashppTypeText(term))
		}
		return strings.Join(terms, " | ")
	case *BashPPApproxType:
		return "~" + bashppTypeText(x.Term)
	case *BashPPCollectionType:
		if x.Kind == "map" {
			return "map[" + bashppTypeText(x.Key) + "]" + bashppTypeText(x.Element)
		}
		length := ""
		if x.Length != nil {
			length = x.Length.Value
		}
		return "[" + length + "]" + bashppTypeText(x.Element)
	case *BashPPPointerType:
		return "*" + bashppTypeText(x.Element)
	case *BashPPStructType:
		return "struct"
	case *BashPPInterfaceType:
		return "interface"
	}
	return ""
}

func bashppCollectionExpr(w *Word) BashPPExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedCollectionAST(expr, false) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

// bashppCompositeExpr lowers both collection and struct composites. Go's
// parser is used only here, while source positions are still available; the
// interpreter never reparses the word.
func bashppCompositeExpr(w *Word) BashPPExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedCompositeAST(expr, false) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

func bashppIndexExpr(w *Word) BashPPExpr {
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil {
		return nil
	}
	switch expr.(type) {
	case *goast.IndexExpr, *goast.SliceExpr, *goast.SelectorExpr:
	default:
		return nil
	}
	if !bashppSupportedPathAST(expr) {
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

func bashppSupportedPathAST(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.ParenExpr:
		return bashppSupportedPathAST(x.X)
	case *goast.StarExpr:
		return bashppSupportedPathAST(x.X)
	case *goast.Ident:
		return bashppIsIdent(x.Name)
	case *goast.SelectorExpr:
		return bashppSupportedPathAST(x.X) && bashppIsIdent(x.Sel.Name)
	case *goast.IndexExpr:
		return bashppSupportedPathAST(x.X) && bashppSupportedScalarAST(x.Index)
	case *goast.SliceExpr:
		return bashppSupportedPathAST(x.X) &&
			(x.Low == nil || bashppSupportedScalarAST(x.Low)) &&
			(x.High == nil || bashppSupportedScalarAST(x.High)) &&
			(x.Max == nil || bashppSupportedScalarAST(x.Max))
	}
	return false
}

func bashppSupportedIndexableAST(expr goast.Expr) bool {
	return bashppSupportedScalarAST(expr) || bashppSupportedCompositeAST(expr, false) || bashppSupportedPathAST(expr)
}

func bashppSupportedCompositeAST(expr goast.Expr, inferred bool) bool {
	x, ok := expr.(*goast.CompositeLit)
	if !ok {
		return false
	}
	if x.Type == nil {
		if !inferred {
			return false
		}
	} else if !bashppSupportedTypeAST(x.Type) {
		return false
	}
	for _, raw := range x.Elts {
		value := raw
		if kv, ok := raw.(*goast.KeyValueExpr); ok {
			// Struct keys are syntactically identifiers even when Go 1.27
			// resolves them as promoted field selectors. Retain a selector-shaped
			// key too so the interpreter can issue a positioned type error once
			// the enclosing composite is known to be a struct; selector
			// expressions remain useful as ordinary map keys.
			if !bashppSupportedScalarAST(kv.Key) && !bashppSupportedPathAST(kv.Key) {
				return false
			}
			value = kv.Value
		}
		if nested, ok := value.(*goast.CompositeLit); ok {
			if !bashppSupportedCompositeAST(nested, true) {
				return false
			}
		} else if !bashppSupportedScalarAST(value) && !bashppSupportedPathAST(value) {
			return false
		}
	}
	return true
}

func bashppSupportedCollectionAST(expr goast.Expr, inferred bool) bool {
	x, ok := expr.(*goast.CompositeLit)
	if !ok {
		return false
	}
	if x.Type == nil {
		if !inferred {
			return false
		}
	} else if !bashppSupportedCollectionTypeAST(x.Type) {
		return false
	} else {
		switch x.Type.(type) {
		case *goast.ArrayType, *goast.MapType:
		default:
			return false
		}
	}
	for _, raw := range x.Elts {
		value := raw
		if kv, ok := raw.(*goast.KeyValueExpr); ok {
			if !bashppSupportedScalarAST(kv.Key) {
				return false
			}
			value = kv.Value
		}
		if nested, ok := value.(*goast.CompositeLit); ok {
			if !bashppSupportedCollectionAST(nested, true) {
				return false
			}
		} else if !bashppSupportedScalarAST(value) {
			return false
		}
	}
	return true
}

func bashppSupportedCollectionTypeAST(expr goast.Expr) bool {
	return bashppSupportedTypeAST(expr)
}

func bashppSupportedTypeAST(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.ChanType:
		return bashppSupportedTypeAST(x.Value)
	case *goast.FuncType:
		return x.TypeParams == nil && bashppSupportedFieldListTypes(x.Params) && bashppSupportedFieldListTypes(x.Results)
	case *goast.Ident:
		return bashppIsIdent(x.Name)
	case *goast.SelectorExpr:
		_, ok := x.X.(*goast.Ident)
		return ok && bashppIsIdent(x.Sel.Name)
	case *goast.IndexExpr:
		name, ok := x.X.(*goast.Ident)
		if ok {
			return bashppIsIdent(name.Name) && bashppSupportedTypeAST(x.Index)
		}
		_, selector := x.X.(*goast.SelectorExpr)
		return selector && bashppSupportedTypeAST(x.X) && bashppSupportedTypeAST(x.Index)
	case *goast.IndexListExpr:
		name, ok := x.X.(*goast.Ident)
		if ok && (!bashppIsIdent(name.Name) || len(x.Indices) == 0) {
			return false
		}
		if !ok {
			if _, selector := x.X.(*goast.SelectorExpr); !selector || len(x.Indices) == 0 || !bashppSupportedTypeAST(x.X) {
				return false
			}
		}
		for _, index := range x.Indices {
			if !bashppSupportedTypeAST(index) {
				return false
			}
		}
		return true
	case *goast.BinaryExpr:
		return x.Op == gotoken.OR && bashppSupportedTypeAST(x.X) && bashppSupportedTypeAST(x.Y)
	case *goast.UnaryExpr:
		return x.Op == gotoken.TILDE && bashppSupportedTypeAST(x.X)
	case *goast.ArrayType:
		if x.Len != nil {
			switch n := x.Len.(type) {
			case *goast.BasicLit:
				if n.Kind != gotoken.INT {
					return false
				}
			case *goast.Ellipsis:
			default:
				if !bashppSupportedScalarAST(n) {
					return false
				}
			}
		}
		return bashppSupportedTypeAST(x.Elt)
	case *goast.MapType:
		return bashppSupportedTypeAST(x.Key) && bashppSupportedTypeAST(x.Value)
	case *goast.StructType:
		for _, field := range x.Fields.List {
			if field.Tag != nil || !bashppSupportedTypeAST(field.Type) {
				return false
			}
			if len(field.Names) == 0 && !bashppSupportedEmbeddedFieldAST(field.Type) {
				return false
			}
			for _, name := range field.Names {
				if !bashppIsIdent(name.Name) {
					return false
				}
			}
		}
		return true
	case *goast.InterfaceType:
		for _, field := range x.Methods.List {
			ft, ok := field.Type.(*goast.FuncType)
			if !ok {
				if len(field.Names) != 0 || !bashppSupportedTypeAST(field.Type) {
					return false
				}
				continue
			}
			if len(field.Names) != 1 || !bashppIsIdent(field.Names[0].Name) {
				return false
			}
			if !bashppSupportedFieldListTypes(ft.Params) || !bashppSupportedFieldListTypes(ft.Results) {
				return false
			}
		}
		return true
	case *goast.StarExpr:
		return bashppSupportedTypeAST(x.X)
	}
	return false
}

func bashppSupportedEmbeddedFieldAST(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.Ident:
		return bashppIsIdent(x.Name)
	case *goast.IndexExpr:
		_, ok := x.X.(*goast.Ident)
		return ok && bashppSupportedTypeAST(x)
	case *goast.IndexListExpr:
		_, ok := x.X.(*goast.Ident)
		return ok && bashppSupportedTypeAST(x)
	case *goast.StarExpr:
		return bashppSupportedEmbeddedFieldAST(x.X)
	}
	return false
}

func bashppGoTypeText(expr goast.Expr) string {
	switch x := expr.(type) {
	case *goast.ChanType:
		prefix := "chan "
		if x.Dir == goast.SEND {
			prefix = "chan<- "
		}
		if x.Dir == goast.RECV {
			prefix = "<-chan "
		}
		return prefix + bashppGoTypeText(x.Value)
	case *goast.FuncType:
		var out bytes.Buffer
		if err := format.Node(&out, gotoken.NewFileSet(), x); err != nil {
			return ""
		}
		return out.String()
	case *goast.Ident:
		return x.Name
	case *goast.SelectorExpr:
		return bashppGoTypeText(x.X) + "." + x.Sel.Name
	case *goast.IndexExpr:
		return bashppGoTypeText(x.X) + "[" + bashppGoTypeText(x.Index) + "]"
	case *goast.IndexListExpr:
		var parts []string
		for _, index := range x.Indices {
			parts = append(parts, bashppGoTypeText(index))
		}
		return bashppGoTypeText(x.X) + "[" + strings.Join(parts, ", ") + "]"
	case *goast.BinaryExpr:
		if x.Op == gotoken.OR {
			return bashppGoTypeText(x.X) + " | " + bashppGoTypeText(x.Y)
		}
	case *goast.UnaryExpr:
		if x.Op == gotoken.TILDE {
			return "~" + bashppGoTypeText(x.X)
		}
	case *goast.ArrayType:
		length := ""
		if x.Len != nil {
			switch n := x.Len.(type) {
			case *goast.BasicLit:
				length = n.Value
			case *goast.Ellipsis:
				length = "..."
			default:
				length = bashppGoExprText(n)
			}
		}
		return "[" + length + "]" + bashppGoTypeText(x.Elt)
	case *goast.MapType:
		return "map[" + bashppGoTypeText(x.Key) + "]" + bashppGoTypeText(x.Value)
	case *goast.StructType:
		return "struct"
	case *goast.StarExpr:
		return "*" + bashppGoTypeText(x.X)
	case *goast.InterfaceType:
		return "interface"
	}
	return ""
}

func bashppSupportedFieldListTypes(fields *goast.FieldList) bool {
	if fields == nil {
		return true
	}
	for _, field := range fields.List {
		if !bashppSupportedTypeAST(field.Type) {
			return false
		}
		for _, name := range field.Names {
			if !bashppIsIdent(name.Name) {
				return false
			}
		}
	}
	return true
}

func bashppGoExprText(expr goast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, gotoken.NewFileSet(), expr); err != nil {
		return ""
	}
	return buf.String()
}

// bashppScalarSource retains a boundary position for every byte passed to the
// Go expression parser. In particular, a single normalized separator may span
// multiple source bytes; adding a byte offset to w.Pos would shift every node
// after that separator to the left.
func bashppScalarSource(w *Word) (string, []Pos, bool) {
	var text strings.Builder
	var positions []Pos
	appendPart := func(value string, start, end Pos, allowCollapsed bool) bool {
		if value == "" {
			return false
		}
		if len(positions) == 0 {
			positions = append(positions, start)
		} else {
			positions[len(positions)-1] = start
		}
		exact := start.Line() == end.Line() && int(end.Offset()-start.Offset()) == len(value)
		if !exact && !allowCollapsed {
			return false
		}
		text.WriteString(value)
		for i := 1; i <= len(value); i++ {
			at := start
			if exact {
				at = posAddCol(start, i)
			} else if i == len(value) {
				at = end
			}
			positions = append(positions, at)
		}
		return true
	}
	for _, part := range w.Parts {
		switch part := part.(type) {
		case *Lit:
			// bashppJoinWords represents an arbitrary source whitespace gap as
			// one space. Its two boundary positions retain the exact gap.
			rawLiteral := len(part.Value) >= 2 && part.Value[0] == '`' && part.Value[len(part.Value)-1] == '`'
			if !appendPart(part.Value, part.Pos(), part.End(), part.Value == " " || rawLiteral) {
				return "", nil, false
			}
		case *SglQuoted, *DblQuoted:
			value := bashppWordText(&Word{Parts: []WordPart{part}})
			// A Go string or rune literal has no recursively represented
			// children, so its exact outer boundaries are sufficient even
			// when shell escaping changes the number of rendered bytes.
			if !appendPart(value, part.Pos(), part.End(), true) {
				return "", nil, false
			}
		default:
			return "", nil, false
		}
	}
	return text.String(), positions, len(positions) == text.Len()+1
}

// bashppCompositeCommandDepth reports an open, supported composite region in
// a completed command prefix. It is used only to carry newlines/semicolons as
// expression whitespace; everything else remains on the ordinary shell path.
func bashppCompositeCommandDepth(ce *CallExpr) int {
	if ce == nil || len(ce.Assigns) != 0 {
		return 0
	}
	words := ce.Args
	eligible := false
	if len(words) >= 4 && words[0].Lit() == "type" {
		if _, _, typeStart, ok := bashppTypeDeclName(words); ok && typeStart+1 < len(words) {
			eligible = (words[typeStart].Lit() == "struct" || words[typeStart].Lit() == "enum" || words[typeStart].Lit() == "interface") && words[typeStart+1].Lit() == "{"
		}
	}
	if !eligible && len(words) >= 5 && words[0].Lit() == "var" &&
		bashppIsIdent(words[1].Lit()) && words[3].Lit() == "=" {
		eligible = strings.Contains(bashppWordText(bashppJoinWords(words[4:])), "{")
	}
	if !eligible {
		for i, w := range words {
			if w.Lit() == ":=" && i > 0 && i+1 < len(words) {
				lhs, ok := bashppShortLHS(words[:i])
				eligible = ok && len(lhs) > 0 && strings.Contains(bashppWordText(bashppJoinWords(words[i+1:])), "{")
				break
			}
		}
	}
	if !eligible {
		return 0
	}
	depth := 0
	for _, w := range words {
		for _, part := range w.Parts {
			if lit, ok := part.(*Lit); ok {
				depth += strings.Count(lit.Value, "{") - strings.Count(lit.Value, "}")
			}
		}
	}
	return depth
}

func bashppCompositeCommandComplete(ce *CallExpr) bool {
	return bashppShortDecl(ce, nil, false) != nil || bashppTypeDecl(ce, nil) != nil || bashppTypedVarDecl(ce, nil) != nil
}

func bashppAssign(ce *CallExpr, redirs []*Redirect, goRegion bool) *BashPPAssign {
	if ce == nil || len(ce.Assigns) != 0 || len(redirs) != 0 || len(ce.Args) < 3 {
		return nil
	}
	eq := -1
	for i, w := range ce.Args {
		if w.Lit() == "=" {
			if eq >= 0 {
				return nil
			}
			eq = i
		}
	}
	if eq < 1 || eq+1 >= len(ce.Args) {
		return nil
	}
	if goRegion {
		if names, ok := bashppShortLHS(ce.Args[:eq]); ok {
			if values, valuesOK := bashppShortValues(ce.Args[eq+1:], true); valuesOK {
				out := &BashPPAssign{Names: names, Values: values, Eq: ce.Args[eq].Pos()}
				for _, value := range values {
					expr := bashppScalarExpr(value)
					if expr == nil {
						out.ValueExprs = nil
						break
					}
					out.ValueExprs = append(out.ValueExprs, expr)
				}
				if len(out.ValueExprs) != 0 || len(names) != 1 {
					return out
				}
			}
			// Once an identifier-list `=` is seen in a committed Go region,
			// malformed or not-yet-supported RHS syntax must not fall through and
			// execute as a shell command. Preserve its words for a positioned
			// runtime EASSIGN-FORM diagnostic.
			if len(names) != 1 || !bashppSupportedValue(bashppJoinWords(ce.Args[eq+1:])) {
				return &BashPPAssign{Names: names, Values: ce.Args[eq+1:], Eq: ce.Args[eq].Pos()}
			}
		}
	}
	for i := 1; i < eq; i++ {
		if ce.Args[i-1].End() != ce.Args[i].Pos() {
			return nil
		}
	}
	target := bashppConcatWords(ce.Args[:eq])
	text := bashppWordText(target)
	value := ce.Args[eq+1]
	if len(ce.Args) > eq+2 {
		value = bashppJoinWords(ce.Args[eq+1:])
	}
	if !bashppSupportedValue(value) {
		return nil
	}
	rhsExpr := bashppScalarExpr(value)
	pointerAssign := false
	if bashppIsIdent(text) {
		switch rhsExpr.(type) {
		case *BashPPAddressExpr, *BashPPNewExpr, *BashPPDerefExpr:
			pointerAssign = true
		}
	}
	if !bashppMutationTarget(text) && !strings.HasPrefix(text, "*") && !(bashppIsIdent(text) && strings.Contains(bashppWordText(value), "{")) && !pointerAssign {
		return nil
	}
	assign := &BashPPAssign{Target: target, Eq: ce.Args[eq].Pos(), Value: value}
	if expr := bashppPointerExpr(target); expr != nil {
		assign.TargetExpr = expr
		if rhs := bashppCompositeExpr(value); rhs != nil {
			assign.ValueExpr = rhs
		} else if rhs := bashppIndexExpr(value); rhs != nil {
			assign.ValueExpr = rhs
		} else {
			assign.ValueExpr = bashppScalarExpr(value)
		}
	} else if pointerAssign {
		assign.ValueExpr = rhsExpr
	}
	return assign
}

func (p *Parser) bashppUpdate(ce *CallExpr, redirs []*Redirect, goRegion bool) Command {
	if !goRegion || ce == nil || len(redirs) != 0 {
		return nil
	}
	if len(ce.Assigns) == 1 && len(ce.Args) == 0 {
		assign := ce.Assigns[0]
		if assign.Append && assign.Name != nil && assign.Value != nil {
			var targetWord *Word
			if assign.Index == nil {
				targetWord = &Word{Parts: []WordPart{assign.Name}}
			} else {
				withoutAppend := *assign
				withoutAppend.Append, withoutAppend.Value = false, nil
				targetWord = p.assignAsWord(&withoutAppend)
			}
			opPos := posAddCol(assign.Value.Pos(), -2)
			return &BashPPUpdate{TargetWord: targetWord, Target: bashppAssignmentTargetExpr(targetWord),
				Op:        &Lit{ValuePos: opPos, ValueEnd: posAddCol(opPos, 2), Value: "+="},
				ValueWord: assign.Value, Value: bashppScalarExpr(assign.Value)}
		}
		return nil
	}
	if len(ce.Assigns) != 0 || len(ce.Args) == 0 {
		return nil
	}
	if inc := bashppStandaloneIncDec(ce.Args); inc != nil {
		// Mixed top-level updates use adjacent x++/x--. A separate token
		// remains a shell argument, independent of the command's name.
		if p.bashppFuncDepth > 0 || inc.TargetWord.End() == inc.Op.Pos() {
			return inc
		}
		return nil
	}
	if len(ce.Args) == 1 {
		for _, part := range ce.Args[0].Parts {
			if _, ok := part.(*Lit); !ok {
				return nil
			}
		}
		text := bashppWordText(ce.Args[0])
		for _, value := range []string{"<<=", ">>=", "&^=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^="} {
			if at := strings.Index(text, value); at > 0 && at+len(value) < len(text) {
				start := ce.Args[0].Pos()
				targetLit := &Lit{ValuePos: start, ValueEnd: posAddCol(start, at), Value: text[:at]}
				opPos := targetLit.End()
				valuePos := posAddCol(opPos, len(value))
				valueLit := &Lit{ValuePos: valuePos, ValueEnd: ce.Args[0].End(), Value: text[at+len(value):]}
				targetWord, valueWord := &Word{Parts: []WordPart{targetLit}}, &Word{Parts: []WordPart{valueLit}}
				if bashppAssignmentTargetExpr(targetWord) == nil {
					return nil
				}
				return &BashPPUpdate{TargetWord: targetWord, Target: bashppAssignmentTargetExpr(targetWord),
					Op:        &Lit{ValuePos: opPos, ValueEnd: valuePos, Value: value},
					ValueWord: valueWord, Value: bashppScalarExpr(valueWord)}
			}
		}
	}
	if len(ce.Args) < 3 {
		return nil
	}
	op := bashppBareLit(ce.Args[1])
	if op == nil {
		return nil
	}
	// Only compound assignment operators claim this start site. A shell
	// argument such as IFS= also ends in '=', including when probing the
	// condition in `while IFS= read ...`; it must retain shell grammar.
	switch op.Value {
	case "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^=":
	default:
		return nil
	}
	switch bashppAssignmentTargetExpr(ce.Args[0]).(type) {
	case *BashPPIdent, *BashPPIndexExpr, *BashPPSelectorExpr, *BashPPDerefExpr, *BashPPParenExpr:
	default:
		return nil
	}
	valueWord := bashppJoinWords(ce.Args[2:])
	return &BashPPUpdate{TargetWord: ce.Args[0], Target: bashppAssignmentTargetExpr(ce.Args[0]), Op: op,
		ValueWord: valueWord, Value: bashppScalarExpr(valueWord)}
}

func bashppStandaloneIncDec(words []*Word) *BashPPIncDec {
	var targetWord *Word
	var op *Lit
	if len(words) == 1 {
		lit := bashppBareLit(words[0])
		if lit == nil {
			return nil
		}
		for _, value := range []string{"++", "--"} {
			if strings.HasSuffix(lit.Value, value) {
				opPos := posAddCol(lit.End(), -2)
				targetLit := &Lit{ValuePos: lit.Pos(), ValueEnd: opPos, Value: strings.TrimSuffix(lit.Value, value)}
				targetWord = &Word{Parts: []WordPart{targetLit}}
				op = &Lit{ValuePos: opPos, ValueEnd: lit.End(), Value: value}
				break
			}
		}
	} else if len(words) == 2 {
		targetWord, op = words[0], bashppBareLit(words[1])
		var splitCompact bool
		if parts := words[0].Parts; len(parts) > 0 {
			if tail, ok := parts[len(parts)-1].(*Lit); ok {
				splitCompact = !tail.Pos().IsValid() && op != nil && tail.Value == op.Value
			}
		}
		if op != nil && (op.Value == "+" || op.Value == "-") && splitCompact {
			firstText := bashppWordText(words[0])
			if !strings.HasSuffix(firstText, op.Value) {
				return nil
			}
			opPos := posAddCol(op.Pos(), -1)
			targetLit := &Lit{ValuePos: words[0].Pos(), ValueEnd: opPos, Value: strings.TrimSuffix(firstText, op.Value)}
			targetWord = &Word{Parts: []WordPart{targetLit}}
			op = &Lit{ValuePos: opPos, ValueEnd: op.End(), Value: op.Value + op.Value}
		} else if op == nil || op.Value != "++" && op.Value != "--" {
			return nil
		}
	}
	if targetWord == nil || op == nil {
		return nil
	}
	target := bashppAssignmentTargetExpr(targetWord)
	if target == nil {
		return nil
	}
	var name *Lit
	if ident, ok := target.(*BashPPIdent); ok {
		name = ident.Name
	}
	return &BashPPIncDec{Name: name, TargetWord: targetWord, Target: target, Op: op}
}

func bashppPointerExpr(w *Word) BashPPExpr {
	if path := bashppIndexExpr(w); path != nil {
		return path
	}
	text, positions, ok := bashppScalarSource(w)
	if !ok {
		return nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil || !bashppSupportedScalarAST(expr) {
		return nil
	}
	switch expr.(type) {
	case *goast.StarExpr, *goast.IndexExpr, *goast.SelectorExpr:
	default:
		return nil
	}
	pos := func(p gotoken.Pos) Pos { return positions[int(p)-1] }
	lit := func(p, end gotoken.Pos, value string) *Lit {
		return &Lit{ValuePos: pos(p), ValueEnd: pos(end), Value: value}
	}
	return bashppConvertExpr(expr, text, pos, lit)
}

func bashppSliceColonOffsets(source string, start, end gotoken.Pos) []int {
	depth := 0
	var out []int
	for i := int(start) - 1; i < int(end)-1 && i < len(source); i++ {
		switch source[i] {
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			depth--
		case ':':
			if depth == 1 {
				out = append(out, i)
			}
		}
	}
	return out
}

func bashppConcatWords(words []*Word) *Word {
	parts := make([]WordPart, 0, len(words)*2)
	for _, word := range words {
		parts = append(parts, word.Parts...)
	}
	return &Word{Parts: parts}
}

func bashppMutationTarget(s string) bool {
	if _, err := goparser.ParseExpr(s); err != nil {
		return false
	}
	root := leadingIdent(s)
	if !bashppIsIdent(root) || len(root) == len(s) {
		return false
	}
	rest := s[len(root):]
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			part := leadingIdent(rest)
			if !bashppIsIdent(part) {
				return false
			}
			rest = rest[len(part):]
		case '[':
			close := strings.IndexByte(rest, ']')
			if close < 2 {
				return false
			}
			rest = rest[close+1:]
		default:
			return false
		}
	}
	return true
}

func bashppShortLHS(words []*Word) ([]*Lit, bool) {
	var out []*Lit
	for i, w := range words {
		l := bashppBareLit(w)
		if l == nil {
			return nil, false
		}
		value := l.Value
		if i < len(words)-1 {
			if !strings.HasSuffix(value, ",") {
				return nil, false
			}
			value = strings.TrimSuffix(value, ",")
		} else if strings.HasSuffix(value, ",") {
			return nil, false
		}
		if !bashppIsIdent(value) {
			return nil, false
		}
		out = append(out, &Lit{ValuePos: l.ValuePos, ValueEnd: posAddCol(l.ValuePos, len(value)), Value: value})
	}
	return out, len(out) > 0
}

func bashppShortValues(words []*Word, goRegion bool) ([]*Word, bool) {
	if len(words) > 1 {
		joined := bashppJoinWords(words)
		if bashppSupportedValue(joined) || goRegion && bashppSupportedScalarExpr(joined) {
			return []*Word{joined}, true
		}
	}
	out := make([]*Word, len(words))
	for i, w := range words {
		wantComma := i < len(words)-1
		clean, comma := bashppTrimComma(w)
		if comma != wantComma || !(bashppSupportedValue(clean) || goRegion && bashppSupportedScalarExpr(clean)) {
			return nil, false
		}
		out[i] = clean
	}
	return out, len(out) > 0
}

func bashppJoinWords(words []*Word) *Word {
	parts := make([]WordPart, 0, len(words)*2-1)
	for i, w := range words {
		if i > 0 {
			// Two words the shell lexer split at an operator sit against each
			// other in the source: `1 <` `=` `2` is `1 <= 2`, not `1 < = 2`.
			// Only a real source gap becomes a separator, so the joined text
			// stays byte-for-byte what was written.
			if prev := words[i-1].End(); prev.Offset() != w.Pos().Offset() {
				parts = append(parts, &Lit{ValuePos: prev, ValueEnd: w.Pos(), Value: " "})
			}
		}
		parts = append(parts, w.Parts...)
	}
	return &Word{Parts: parts}
}

func bashppTrimComma(w *Word) (*Word, bool) {
	if w == nil || len(w.Parts) == 0 {
		return w, false
	}
	last, ok := w.Parts[len(w.Parts)-1].(*Lit)
	if !ok || !strings.HasSuffix(last.Value, ",") {
		return w, false
	}
	copyWord := *w
	copyWord.Parts = append([]WordPart(nil), w.Parts...)
	copyLit := *last
	copyLit.Value = strings.TrimSuffix(copyLit.Value, ",")
	copyLit.ValueEnd = posAddCol(copyLit.ValueEnd, -1)
	if copyLit.Value == "" {
		copyWord.Parts = copyWord.Parts[:len(copyWord.Parts)-1]
	} else {
		copyWord.Parts[len(copyWord.Parts)-1] = &copyLit
	}
	return &copyWord, true
}

func bashppSupportedValue(w *Word) bool {
	if bashppInitKind(w) != "" {
		return true
	}
	if len(w.Parts) == 1 {
		switch q := w.Parts[0].(type) {
		case *SglQuoted:
			return !q.Dollar
		case *DblQuoted:
			for _, part := range q.Parts {
				if _, ok := part.(*Lit); !ok {
					return false
				}
			}
			return true
		}
	}
	text := bashppWordText(w)
	if bashppIsIdent(text) {
		return true
	}
	if strings.Contains(text, ".") && bashppSelector(text) {
		return true
	}
	if open := strings.IndexByte(text, '{'); open >= 0 {
		if !strings.HasSuffix(text, "}") || !bashppCompositeType(text[:open]) ||
			!bashppBalanced(text[open:], '{', '}') {
			return false
		}
		_, err := goparser.ParseExpr(text)
		return err == nil
	}
	if open := strings.IndexByte(text, '['); open > 0 {
		return strings.HasSuffix(text, "]") && bashppSelector(text[:open]) &&
			bashppNonemptyTypeArgs(text[open:])
	}
	return false
}

func bashppSupportedScalarExpr(w *Word) bool {
	return bashppScalarExpr(w) != nil
}

func bashppSupportedScalarAST(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.BasicLit:
		switch x.Kind {
		case gotoken.INT, gotoken.FLOAT, gotoken.CHAR, gotoken.STRING:
			return true
		}
	case *goast.Ident:
		return bashppIsIdent(x.Name) || x.Name == "true" || x.Name == "false" || x.Name == "nil"
	case *goast.ParenExpr:
		return bashppSupportedScalarAST(x.X)
	case *goast.UnaryExpr:
		switch x.Op {
		case gotoken.ADD, gotoken.SUB, gotoken.NOT, gotoken.XOR, gotoken.MUL:
			return bashppSupportedScalarAST(x.X)
		case gotoken.AND:
			return bashppSupportedAddressAST(x.X)
		}
	case *goast.StarExpr:
		return bashppSupportedScalarAST(x.X)
	case *goast.BinaryExpr:
		switch x.Op {
		// The first row is spelled with characters the shell lexer keeps
		// inside a word; the rest are shell metacharacters, and reach here
		// only through [Parser.bashppScalarTail].
		case gotoken.EQL, gotoken.NEQ, gotoken.ADD, gotoken.SUB,
			gotoken.XOR, gotoken.MUL, gotoken.QUO, gotoken.REM,
			gotoken.LOR, gotoken.LAND, gotoken.LSS, gotoken.LEQ,
			gotoken.GTR, gotoken.GEQ, gotoken.OR, gotoken.AND,
			gotoken.AND_NOT, gotoken.SHL, gotoken.SHR:
			return bashppSupportedScalarAST(x.X) && bashppSupportedScalarAST(x.Y)
		}
	case *goast.CallExpr:
		if x.Ellipsis.IsValid() {
			return false
		}
		id, ok := x.Fun.(*goast.Ident)
		if !ok || !bashppIsIdent(id.Name) {
			return false
		}
		if id.Name == "new" {
			return len(x.Args) == 1 && bashppSupportedTypeAST(x.Args[0])
		}
		if bashppScalarConversionType(id.Name) || id.Name == "len" || id.Name == "cap" {
			return len(x.Args) == 1 && bashppSupportedScalarAST(x.Args[0])
		}
		for _, arg := range x.Args {
			if !bashppSupportedScalarAST(arg) {
				return false
			}
		}
		return true
	case *goast.IndexExpr:
		return bashppSupportedIndexableAST(x.X) && bashppSupportedScalarAST(x.Index)
	case *goast.SliceExpr:
		return bashppSupportedIndexableAST(x.X) &&
			(x.Low == nil || bashppSupportedScalarAST(x.Low)) &&
			(x.High == nil || bashppSupportedScalarAST(x.High)) &&
			(x.Max == nil || bashppSupportedScalarAST(x.Max))
	case *goast.SelectorExpr:
		return bashppContainsDeref(x.X) && bashppSupportedScalarAST(x.X) && bashppIsIdent(x.Sel.Name)
	case *goast.TypeAssertExpr:
		return bashppSupportedScalarAST(x.X) && x.Type != nil && bashppSupportedTypeAST(x.Type)
	}
	return false
}

func bashppContainsDeref(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.StarExpr:
		return true
	case *goast.ParenExpr:
		return bashppContainsDeref(x.X)
	case *goast.SelectorExpr:
		return bashppContainsDeref(x.X)
	case *goast.IndexExpr:
		return bashppContainsDeref(x.X)
	case *goast.SliceExpr:
		return bashppContainsDeref(x.X)
	}
	return false
}

func bashppSupportedAddressAST(expr goast.Expr) bool {
	switch x := expr.(type) {
	case *goast.Ident:
		return bashppIsIdent(x.Name)
	case *goast.SelectorExpr:
		return bashppSupportedPathAST(x)
	case *goast.IndexExpr:
		return bashppSupportedPathAST(x)
	case *goast.ParenExpr:
		return bashppSupportedAddressAST(x.X)
	}
	// Keep syntactically valid operands in the typed tree so the interpreter
	// can issue the deterministic non-addressable diagnostic.
	return bashppSupportedScalarAST(expr)
}

func bashppScalarConversionType(name string) bool {
	switch name {
	case "bool", "byte", "float32", "float64", "int", "int8", "int16",
		"int32", "int64", "rune", "string", "uint", "uint8", "uint16",
		"uint32", "uint64", "uintptr":
		return true
	}
	return false
}

func bashppCompositeType(s string) bool {
	expr, err := goparser.ParseExpr(s + "{}")
	if err != nil {
		return false
	}
	lit, ok := expr.(*goast.CompositeLit)
	if !ok || !bashppSupportedTypeAST(lit.Type) {
		return bashppSelector(s) // retained for the deferred struct path
	}
	switch lit.Type.(type) {
	case *goast.ArrayType, *goast.MapType, *goast.Ident, *goast.IndexExpr, *goast.IndexListExpr:
		return true
	}
	return bashppSelector(s)
}

func bashppNonemptyTypeArgs(s string) bool {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' || !bashppBalanced(s, '[', ']') {
		return false
	}
	for _, arg := range strings.Split(s[1:len(s)-1], ",") {
		if !bashppSelector(strings.TrimSpace(arg)) {
			return false
		}
	}
	return true
}

func bashppBalanced(s string, open, close byte) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func bashppWordText(w *Word) string {
	var b bytes.Buffer
	_ = NewPrinter().Print(&b, w)
	return b.String()
}

func shortDeclHead(lhs []*Lit) string {
	var b strings.Builder
	for i, l := range lhs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(l.Value)
	}
	return b.String()
}

func bashppSelector(s string) bool {
	return s != "" && leadingSelector(s) == s
}

func bashppSelectorLits(name *Lit) []*Lit {
	parts := strings.Split(name.Value, ".")
	out := make([]*Lit, len(parts))
	off := 0
	for i, part := range parts {
		pos := posAddCol(name.ValuePos, off)
		out[i] = &Lit{ValuePos: pos, ValueEnd: posAddCol(pos, len(part)), Value: part}
		off += len(part) + 1
	}
	return out
}

// parserTransaction records reads beyond the lexer's current buffer and keeps
// a complete parser snapshot. Rollback restores both, including the original
// byte positions and pending diagnostic state.
type parserTransaction struct {
	saved Parser
	src   io.Reader
	read  *recordingReader
	bsLen int
}

type recordingReader struct {
	r      io.Reader
	b      bytes.Buffer
	record bool
}

func (r *recordingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.record {
		_, _ = r.b.Write(p[:n])
	}
	return n, err
}

func (p *Parser) beginBashPPTxn() *parserTransaction {
	t := &parserTransaction{saved: *p, src: p.src, bsLen: len(p.bs)}
	t.read = &recordingReader{r: p.src, record: true}
	p.src = t.read
	return t
}

func (t *parserTransaction) commit(p *Parser) {
	// Keep p.src intact: a transaction checkpoint may have installed replay
	// bytes in front of the recorder. Disabling the recorder and reconnecting
	// it to the original source preserves those bytes without retaining any
	// transactional behavior after commit.
	t.read.r = t.src
	t.read.record = false
}

type parserTxnCheckpoint struct {
	saved   Parser
	bsLen   int
	readLen int
}

func (t *parserTransaction) checkpoint(p *Parser) parserTxnCheckpoint {
	return parserTxnCheckpoint{saved: *p, bsLen: len(p.bs), readLen: t.read.b.Len()}
}

func (t *parserTransaction) rewind(p *Parser, c parserTxnCheckpoint) {
	read := append([]byte(nil), t.read.b.Bytes()[c.readLen:]...)
	*p = c.saved
	if c.bsLen == 0 {
		p.bs = nil
	} else {
		p.bs = p.readBuf[:c.bsLen]
	}
	if len(read) > 0 {
		p.src = io.MultiReader(bytes.NewReader(read), t.read)
	} else {
		p.src = t.read
	}
}

func (t *parserTransaction) rollback(p *Parser) {
	*p = t.saved
	if t.bsLen == 0 {
		p.bs = nil
	} else {
		p.bs = p.readBuf[:t.bsLen]
	}
	if t.read.b.Len() > 0 {
		p.src = io.MultiReader(bytes.NewReader(t.read.b.Bytes()), t.src)
	} else {
		p.src = t.src
	}
}

// bashppParenForm recognizes a Class-R call transactionally. Any malformed or
// unsupported candidate is rewound and left to the ordinary Bash parser.
func (p *Parser) bashppParenForm(ce *CallExpr) Command {
	if ce == nil || len(ce.Assigns) != 0 || len(ce.Args) == 0 || p.tok != leftParen || p.spaced {
		return nil
	}
	// A `func` in callee position is a function LITERAL, not a call to
	// something named func — `func` is a Go keyword and can never be a callee.
	// It is claimed here rather than at a fourth call site in parser.go
	// because every literal site reaches this same point.
	if cmd := p.bashppFuncLitForm(ce); cmd != nil {
		return cmd
	}
	var name *Lit
	var lhs []*Lit
	var opPos Pos
	short := false
	assignNew := false
	assignCall := false
	var assignTarget *Word
	var returnKw *Lit
	if len(ce.Args) >= 3 {
		op := len(ce.Args) - 2
		opLit, funLit := bashppBareLit(ce.Args[op]), bashppWordLit(ce.Args[op+1])
		var ok bool
		if opLit != nil && opLit.Value == "=" && funLit != nil {
			lhs, ok = bashppShortLHS(ce.Args[:op])
			if !ok {
				return nil
			}
			name, opPos, assignCall = funLit, opLit.Pos(), true
			if len(lhs) == 1 {
				assignTarget = ce.Args[0]
			}
			assignNew = funLit.Value == "new"
		} else if opLit == nil || opLit.Value != ":=" || funLit == nil {
			return nil
		} else {
			lhs, ok = bashppShortLHS(ce.Args[:op])
			if !ok {
				return nil
			}
			name, opPos, short = funLit, opLit.Pos(), true
		}
	} else if len(ce.Args) == 2 && p.bashppFuncDepth > 0 && bashppBareLit(ce.Args[0]) != nil && bashppBareLit(ce.Args[0]).Value == "return" {
		returnKw = bashppBareLit(ce.Args[0])
		name = bashppWordLit(ce.Args[1])
	} else if len(ce.Args) == 1 {
		name = bashppWordLit(ce.Args[0])
	} else {
		return nil
	}
	if short && name != nil && strings.HasSuffix(name.Value, ".") {
		txn := p.beginBashPPTxn()
		lparen := p.pos
		p.next()
		typeWord := p.getWord()
		typeLit := bashppBareLit(typeWord)
		typ := bashppTypeExpr(typeWord)
		typeToken := typeLit != nil && typeLit.Value == "type"
		if (!typeToken && typ == nil) || p.tok != rightParen {
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
		rootName := strings.TrimSuffix(name.Value, ".")
		root := &Lit{ValuePos: name.Pos(), ValueEnd: posAddCol(name.End(), -1), Value: rootName}
		expr := &BashPPTypeAssertExpr{
			X:      &BashPPIdent{Name: root},
			Dot:    root.End(),
			Lparen: lparen,
			Assert: typ,
			Rparen: rparen,
		}
		if typeToken {
			expr.TypeToken = typeLit
		}
		value := rootName + ".(" + bashppWordText(typeWord) + ")"
		rhs := &Word{Parts: []WordPart{&Lit{ValuePos: name.Pos(), ValueEnd: posAddCol(rparen, 1), Value: value}}}
		return &BashPPShortDecl{Lhs: lhs, Rhs: []*Word{rhs}, Class: ClassR, OpPos: opPos, GoRegion: true, Expr: expr}
	}
	var typeArgs []*BashPPTypeArg
	if name != nil {
		var ok bool
		name, typeArgs, ok = bashppCallTypeArgs(name)
		if !ok {
			return nil
		}
	}
	if name == nil || !bashppSelector(name.Value) {
		return nil
	}
	if !short && !assignCall && !strings.Contains(name.Value, ".") && p.r == ')' && !p.bashppCallable(name.Value) {
		// A zero-argument bare call is indistinguishable from the head of a
		// shell function declaration until its name has been declared. Keep
		// unknown names in the shell grammar; source-block declarations add
		// their public names to the same parser-local callable set.
		return nil
	}
	txn := p.beginBashPPTxn()
	lparen := p.pos
	p.next()
	// `make(chan T, n)` is not an ordinary call: its first argument is a TYPE,
	// which no argument list can hold, so it gets its own reader. Like every
	// other channel form it uses the existing typed channel reader, including
	// at mixed top level. A `make(` the channel grammar does
	// not spell rewinds to the shell, which keeps `make(1)` and the GNU make
	// command exactly as they are.
	if short && name.Value == "make" {
		makeTxn := p.beginBashPPTxn()
		mk := p.bashppMakeChanTail(name, lparen)
		if mk == nil {
			makeTxn.rollback(p)
		} else {
			makeTxn.commit(p)
			txn.commit(p)
			return &BashPPShortDecl{Lhs: lhs, Class: ClassR, OpPos: opPos, GoRegion: true, MakeChan: mk}
		}
	}
	if (short || (assignNew && len(lhs) == 1)) && name.Value == "new" {
		newTxn := p.beginBashPPTxn()
		typeWord := p.getWord()
		var typ BashPPTypeExpr
		if typeWord != nil {
			typ = bashppTypeExpr(typeWord)
		}
		if typ == nil || p.tok != rightParen {
			newTxn.rollback(p)
		} else {
			rparen := p.pos
			p.next()
			if !bashppCallTerminator(p.tok) {
				newTxn.rollback(p)
			} else {
				newTxn.commit(p)
				txn.commit(p)
				newExpr := &BashPPNewExpr{New: name, Lparen: lparen, AllocType: typ, Rparen: rparen}
				if assignNew {
					value := &Word{Parts: []WordPart{&Lit{ValuePos: name.Pos(), ValueEnd: posAddCol(rparen, 1), Value: "new(" + bashppWordText(typeWord) + ")"}}}
					return &BashPPAssign{Target: assignTarget, Eq: opPos, Value: value, ValueExpr: newExpr}
				}
				return &BashPPShortDecl{Lhs: lhs, Class: ClassR, OpPos: opPos, GoRegion: true,
					Expr: newExpr}
			}
		}
	}
	var argTypes []BashPPTypeExpr
	var args []*Word
	var argNames []*Lit
	var ellipsis Pos
	var ok bool
	if (short || assignCall) && name.Value == "make" {
		makeTxn := p.beginBashPPTxn()
		args, argTypes, ok = p.bashppMakeValueArgs()
		if ok {
			makeTxn.commit(p)
		} else {
			makeTxn.rollback(p)
			args, argNames, ellipsis, ok = p.bashppCallArgs()
		}
	} else {
		args, argNames, ellipsis, ok = p.bashppCallArgs()
	}
	if !ok || p.tok != rightParen {
		txn.rollback(p)
		return nil
	}
	rparen := p.pos
	p.next()
	if !bashppCallTerminator(p.tok) && !(p.bashppFuncDepth > 0 && p.tok == _LitWord && p.val == "}") {
		txn.rollback(p)
		return nil
	}
	txn.commit(p)
	call := &BashPPCall{
		Fun: bashppSelectorLits(name), TypeArgs: typeArgs, Args: args, ArgNames: argNames, Ellipsis: ellipsis,
		Lparen: lparen, Rparen: rparen,
	}
	if len(argTypes) > 0 {
		call.ArgType = argTypes[0]
	}
	if assignCall || returnKw != nil {
		var text strings.Builder
		text.WriteString(name.Value)
		text.WriteByte('(')
		for i, arg := range args {
			if i > 0 {
				text.WriteString(", ")
			}
			text.WriteString(bashppWordText(arg))
		}
		if ellipsis.IsValid() {
			text.WriteString("...")
		}
		text.WriteByte(')')
		value := &Word{Parts: []WordPart{&Lit{ValuePos: name.Pos(), ValueEnd: call.End(), Value: text.String()}}}
		if returnKw != nil {
			return &BashPPReturn{Kw: returnKw, Results: []*Word{value}, Call: call}
		}
		if len(lhs) > 1 {
			return &BashPPAssign{Names: lhs, Eq: opPos, Value: value, Call: call}
		}
		return &BashPPAssign{Target: assignTarget, Eq: opPos, Value: value, Call: call}
	}
	if short && len(call.Fun) == 1 && len(call.Args) == 1 && len(call.ArgNames) == 0 &&
		!call.Ellipsis.IsValid() && bashppScalarConversionType(call.Fun[0].Value) {
		if arg := bashppScalarExpr(call.Args[0]); arg != nil {
			return &BashPPShortDecl{Lhs: lhs, Class: ClassR, OpPos: opPos,
				GoRegion: true,
				Expr:     &BashPPConvertExpr{ConvType: call.Fun[0], Lparen: call.Lparen, X: arg, Rparen: call.Rparen}}
		}
	}
	if !short {
		// `close(ch)` is the Go builtin, not a call the interpreter can
		// dispatch to a declared function, so it gets its own node rather
		// than a BashPPCall a later phase would have to special-case by name.
		// Inside a func body `close` cannot also be a user function: it is a
		// Go builtin identifier there, exactly as it is in Go.
		if p.bashppFuncDepth > 0 && len(call.Fun) == 1 && call.Fun[0].Value == "close" &&
			len(args) == 1 && len(argNames) == 0 && !ellipsis.IsValid() &&
			bashppChanOperand(args[0]) {
			return &BashPPClose{Kw: name, Chan: args[0], Lparen: lparen, Rparen: rparen}
		}
		return call
	}
	// A name bound from a call may hold a closure — that is the factory idiom,
	// `next := counter()` — so it joins the callable names for the rest of the
	// parse. The claim is narrow in practice: it only decides the ZERO-argument
	// `next()`, which bash rejects outright, and the call form still rewinds
	// when a body follows, so `next() { … }` and `next() ( … )` stay the shell
	// function definitions they are today.
	for _, lit := range lhs {
		p.bashppRegisterFunc(lit.Value)
	}
	var text strings.Builder
	text.WriteString(name.Value)
	if len(typeArgs) > 0 {
		text.WriteByte('[')
		for i, arg := range typeArgs {
			if i > 0 {
				text.WriteString(", ")
			}
			text.WriteString(bashppTypeText(arg.ArgType))
		}
		text.WriteByte(']')
	}
	text.WriteByte('(')
	for i, arg := range args {
		if i > 0 {
			text.WriteString(", ")
		}
		text.WriteString(bashppWordText(arg))
	}
	if ellipsis.IsValid() {
		text.WriteString("...")
	}
	text.WriteByte(')')
	rhs := &Word{Parts: []WordPart{&Lit{ValuePos: name.Pos(), ValueEnd: call.End(), Value: text.String()}}}
	return &BashPPShortDecl{Lhs: lhs, Rhs: []*Word{rhs}, Class: ClassR, OpPos: opPos, GoRegion: true, Call: call}
}

// bashppMakeValueArgs reads make(T[, n[, cap]]) for slice and map T. Channel
// make remains owned by BashPPMakeChan and is attempted first by the caller.
func (p *Parser) bashppMakeValueArgs() ([]*Word, []BashPPTypeExpr, bool) {
	if p.tok == rightParen {
		return nil, nil, true
	}
	typeWord := p.getWord()
	if typeWord == nil {
		return nil, nil, false
	}
	clean, comma := bashppTrimComma(typeWord)
	typ := bashppTypeExpr(clean)
	if typ == nil {
		return nil, nil, false
	}
	args := []*Word{clean}
	types := []BashPPTypeExpr{typ}
	if p.tok == rightParen {
		return args, types, !comma
	}
	if !comma {
		return nil, nil, false
	}
	rest, names, ellipsis, ok := p.bashppCallArgs()
	if !ok || len(names) != 0 || ellipsis.IsValid() {
		return nil, nil, false
	}
	args = append(args, rest...)
	types = append(types, make([]BashPPTypeExpr, len(rest))...)
	return args, types, true
}

func bashppWordLit(w *Word) *Lit {
	if lit := bashppBareLit(w); lit != nil {
		return lit
	}
	text := bashppWordText(w)
	if text == "" {
		return nil
	}
	return &Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: text}
}

func bashppCallTerminator(tok token) bool {
	switch tok {
	case _EOF, _Newl, semicolon:
		return true
	}
	return false
}

// bashppCallArgs reads a call's argument list up to the closing parenthesis,
// returning the position of a trailing `...` when the final argument spreads a
// slice into a variadic parameter. Go allows the dots only on the last
// argument, so one position describes the whole list.
func (p *Parser) bashppCallArgs() ([]*Word, []*Lit, Pos, bool) {
	if p.tok == rightParen {
		return nil, nil, Pos{}, true
	}
	var args []*Word
	var names []*Lit
	named := false
	for {
		w := p.getWord()
		if w == nil {
			return nil, nil, Pos{}, false
		}
		clean, comma := bashppTrimComma(w)
		var name *Lit
		if lit := bashppBareLit(clean); lit != nil && strings.HasSuffix(lit.Value, ":") {
			value := strings.TrimSuffix(lit.Value, ":")
			if comma || !bashppIsIdent(value) {
				return nil, nil, Pos{}, false
			}
			name = &Lit{ValuePos: lit.Pos(), ValueEnd: posAddCol(lit.End(), -1), Value: value}
			clean = p.getWord()
			if clean == nil {
				return nil, nil, Pos{}, false
			}
			clean, comma = bashppTrimComma(clean)
			named = true
		} else if named {
			// Bash# follows Python's positional-before-named rule. Shapes which
			// reverse it are outside the accepted grammar and remain shell input.
			return nil, nil, Pos{}, false
		}
		clean, ellipsis := bashppTrimEllipsis(clean)
		if !bashppCallArg(clean) {
			return nil, nil, Pos{}, false
		}
		if name != nil && ellipsis.IsValid() {
			return nil, nil, Pos{}, false
		}
		args = append(args, clean)
		if name != nil {
			names = append(names, name)
		}
		if ellipsis.IsValid() && (comma || p.tok != rightParen) {
			// `f(xs..., y)` — the dots were not on the final argument.
			return nil, nil, Pos{}, false
		}
		if p.tok == rightParen {
			return args, names, ellipsis, !comma
		}
		if !comma {
			return nil, nil, Pos{}, false
		}
	}
}

// bashppTrimEllipsis splits a trailing `...` off an argument word, returning
// the word without it and the position the dots occupied. It mirrors
// [bashppTrimComma]: both peel a piece of punctuation the shell lexer has
// glued onto the end of a word, and both must leave the word's own positions
// intact so the printer can put the source back together.
func bashppTrimEllipsis(w *Word) (*Word, Pos) {
	if w == nil || len(w.Parts) == 0 {
		return w, Pos{}
	}
	last, ok := w.Parts[len(w.Parts)-1].(*Lit)
	if !ok || !strings.HasSuffix(last.Value, "...") {
		return w, Pos{}
	}
	if last.Value == "..." && len(w.Parts) == 1 {
		// A bare `...` spreads nothing; leave it to the shell.
		return w, Pos{}
	}
	pos := posAddCol(last.ValueEnd, -3)
	copyWord := *w
	copyWord.Parts = append([]WordPart(nil), w.Parts...)
	copyLit := *last
	copyLit.Value = strings.TrimSuffix(copyLit.Value, "...")
	copyLit.ValueEnd = pos
	if copyLit.Value == "" {
		copyWord.Parts = copyWord.Parts[:len(copyWord.Parts)-1]
	} else {
		copyWord.Parts[len(copyWord.Parts)-1] = &copyLit
	}
	return &copyWord, pos
}

func bashppCallArg(w *Word) bool {
	if bashppSupportedValue(w) {
		return true
	}
	if l := bashppBareLit(w); l != nil {
		return bashppIsIdent(l.Value)
	}
	// A call that actually runs needs to pass values, not only literals, so a
	// parameter expansion (`$x`), an arithmetic expansion (`$((n-1))`) or a
	// quoted word carrying one is a valid argument. Command and process
	// substitution stay excluded: they run a command, and the published call
	// grammar keeps a Class R argument list side-effect-free, so `f($(x))`
	// rolls back to the shell rather than being claimed.
	return bashppArgWordSafe(w)
}

// bashppArgWordSafe reports whether every part of w is a value-producing form
// that does not execute a command. It is recursive so a double-quoted word is
// only safe when its own parts are.
func bashppArgWordSafe(w *Word) bool {
	if len(w.Parts) == 0 {
		return false
	}
	for _, part := range w.Parts {
		if !bashppArgPartSafe(part) {
			return false
		}
	}
	return true
}

func bashppArgPartSafe(part WordPart) bool {
	switch part := part.(type) {
	case *Lit, *SglQuoted, *ParamExp, *ArithmExp:
		return true
	case *DblQuoted:
		for _, inner := range part.Parts {
			if !bashppArgPartSafe(inner) {
				return false
			}
		}
		return true
	}
	return false
}
