// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"fmt"
	"io"
	"reflect"
)

// Walk traverses a syntax tree in depth-first order: It starts by calling
// f(node); node must not be nil. If f returns true, Walk invokes f
// recursively for each of the non-nil children of node, followed by
// f(nil).
func Walk(node Node, f func(Node) bool) {
	if !f(node) {
		return
	}

	switch node := node.(type) {
	case *File:
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *Comment:
	case *Stmt:
		for _, c := range node.Comments {
			if !node.End().After(c.Pos()) {
				defer Walk(&c, f)
				break
			}
			Walk(&c, f)
		}
		if node.Cmd != nil {
			Walk(node.Cmd, f)
		}
		walkList(node.Redirs, f)
	case *Assign:
		walkNilable(node.Name, f)
		walkNilable(node.Value, f)
		walkNilable(node.Index, f)
		walkNilable(node.Array, f)
	case *Redirect:
		walkNilable(node.N, f)
		Walk(node.Word, f)
		walkNilable(node.Hdoc, f)
	case *CallExpr:
		walkList(node.Assigns, f)
		walkList(node.Args, f)
	case *Subshell:
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *Block:
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *IfClause:
		walkList(node.Cond, f)
		walkComments(node.CondLast, f)
		walkList(node.Then, f)
		walkComments(node.ThenLast, f)
		walkNilable(node.Else, f)
	case *WhileClause:
		walkList(node.Cond, f)
		walkComments(node.CondLast, f)
		walkList(node.Do, f)
		walkComments(node.DoLast, f)
	case *ForClause:
		Walk(node.Loop, f)
		walkList(node.Do, f)
		walkComments(node.DoLast, f)
	case *WordIter:
		Walk(node.Name, f)
		walkList(node.Items, f)
	case *CStyleLoop:
		walkNilable(node.Init, f)
		walkNilable(node.Cond, f)
		walkNilable(node.Post, f)
	case *BinaryCmd:
		Walk(node.X, f)
		Walk(node.Y, f)
	case *FuncDecl:
		walkList(node.Decorators, f)
		walkNilable(node.Agentic, f)
		walkNilable(node.Name, f)
		walkList(node.Names, f)
		Walk(node.Body, f)
	case *Word:
		walkList(node.Parts, f)
	case *Lit:
	case *SglQuoted:
	case *DblQuoted:
		walkList(node.Parts, f)
	case *CmdSubst:
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *ParamExp:
		walkNilable(node.Flags, f)
		walkNilable(node.Param, f)
		walkNilable(node.NestedParam, f)
		walkNilable(node.Index, f)
		if node.Slice != nil {
			walkNilable(node.Slice.Offset, f)
			walkNilable(node.Slice.Length, f)
		}
		if node.Repl != nil {
			walkNilable(node.Repl.Orig, f)
			walkNilable(node.Repl.With, f)
		}
		if node.Exp != nil {
			walkNilable(node.Exp.Word, f)
		}
		walkNilable(node.BadSubst, f)
	case *ArithmExp:
		// An empty arithmetic expression or command — `$(())` and `(())` —
		// parses with no inner expression, so X may be nil here.
		walkNilable(node.X, f)
	case *ArithmCmd:
		walkNilable(node.X, f)
	case *BinaryArithm:
		Walk(node.X, f)
		Walk(node.Y, f)
	case *BinaryTest:
		Walk(node.X, f)
		Walk(node.Y, f)
	case *UnaryArithm:
		Walk(node.X, f)
	case *UnaryTest:
		Walk(node.X, f)
	case *ParenArithm:
		Walk(node.X, f)
	case *FlagsArithm:
		Walk(node.Flags, f)
		if node.X != nil {
			Walk(node.X, f)
		}
	case *ParenTest:
		Walk(node.X, f)
	case *CaseClause:
		Walk(node.Word, f)
		walkList(node.Items, f)
		walkComments(node.Last, f)
	case *CaseItem:
		for _, c := range node.Comments {
			if c.Pos().After(node.Pos()) {
				defer Walk(&c, f)
				break
			}
			Walk(&c, f)
		}
		walkList(node.Patterns, f)
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *TestClause:
		Walk(node.X, f)
	case *DeclClause:
		walkList(node.Args, f)
	case *ArrayExpr:
		walkList(node.Elems, f)
		walkComments(node.Last, f)
	case *ArrayElem:
		for _, c := range node.Comments {
			if c.Pos().After(node.Pos()) {
				defer Walk(&c, f)
				break
			}
			Walk(&c, f)
		}
		walkNilable(node.Index, f)
		walkNilable(node.Value, f)
	case *ExtGlob:
		Walk(node.Pattern, f)
	case *ProcSubst:
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *TimeClause:
		walkNilable(node.Stmt, f)
	case *CoprocClause:
		walkNilable(node.Name, f)
		Walk(node.Stmt, f)
	case *LetClause:
		walkList(node.Exprs, f)
	case *TestDecl:
		Walk(node.Description, f)
		Walk(node.Body, f)
	case *SourceBlock:
		Walk(node.Language, f)
		walkNilable(node.Alias, f)
		walkNilable(node.Runner, f)
		walkNilable(node.Src, f)
	case *BashPPDecl:
		// The Bash++ nodes live in bashpp_nodes.go, but Walk is the one place
		// they cannot: a Command the visitor does not know reaches the panic
		// below. Every P1 node the dispatch constructs is handled here — the
		// cases that follow cover every constructed Bash++ form.
		walkNilable(node.Kw, f)
		walkNilable(node.Name, f)
		walkList(node.TypeParams, f)
		walkNilable(node.DeclType, f)
		walkList(node.Init, f)
		if node.DeclTypeExpr != nil {
			Walk(node.DeclTypeExpr, f)
		} else {
			walkList(node.StructFields, f)
		}
		walkNilable(node.InitExpr, f)
		walkList(node.EnumMembers, f)
	case *BashPPConstGroup:
		walkNilable(node.Kw, f)
		walkComments(node.Comments, f)
		walkList(node.Specs, f)
		walkComments(node.Last, f)
	case *BashPPConstSpec:
		walkComments(node.Comments, f)
		walkNilable(node.Name, f)
		walkNilable(node.DeclType, f)
		walkNilable(node.DeclTypeExpr, f)
		walkList(node.Init, f)
		walkNilable(node.InitExpr, f)
	case *BashPPIf:
		walkNilable(node.InitStmt, f)
		walkNilable(node.Init, f)
		walkNilable(node.Cond, f)
		walkNilable(node.Then, f)
		walkNilable(node.Else, f)
	case *BashPPFor:
		walkNilable(node.Init, f)
		walkNilable(node.Cond, f)
		walkNilable(node.Post, f)
		walkNilable(node.Body, f)
	case *BashPPForAssign:
		walkNilable(node.Name, f)
		walkNilable(node.Expr, f)
	case *BashPPIncDec:
		walkNilable(node.Name, f)
		walkNilable(node.TargetWord, f)
		walkNilable(node.Target, f)
		walkNilable(node.Op, f)
	case *BashPPUpdate:
		walkNilable(node.TargetWord, f)
		walkNilable(node.Target, f)
		walkNilable(node.Op, f)
		walkNilable(node.ValueWord, f)
		walkNilable(node.Value, f)
	case *BashPPBranch:
		walkNilable(node.Kw, f)
	case *BashPPLabeled:
		walkNilable(node.Label, f)
		walkNilable(node.Stmt, f)
	case *BashPPGoto:
		walkNilable(node.Kw, f)
		walkNilable(node.Label, f)
	case *BashPPSwitch:
		walkNilable(node.Init, f)
		walkNilable(node.Tag, f)
		walkList(node.Arms, f)
	case *BashPPSwitchArm:
		walkList(node.Exprs, f)
		walkList(node.Types, f)
		walkList(node.Commas, f)
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *BashPPAssign:
		walkList(node.Names, f)
		walkList(node.Values, f)
		walkList(node.ValueExprs, f)
		walkNilable(node.Target, f)
		walkNilable(node.Value, f)
		walkNilable(node.TargetExpr, f)
		walkNilable(node.ValueExpr, f)
		walkNilable(node.Call, f)
		walkNilable(node.Recv, f)
	case *BashPPShortDecl:
		walkList(node.RhsExprs, f)
		walkList(node.Lhs, f)
		walkList(node.Rhs, f)
		walkList(node.MethodValue, f)
		if node.Call != nil {
			Walk(node.Call, f)
		}
		if node.FuncLit != nil {
			Walk(node.FuncLit, f)
		}
		if node.Recv != nil {
			Walk(node.Recv, f)
		}
		if node.MakeChan != nil {
			Walk(node.MakeChan, f)
		}
		walkNilable(node.Expr, f)
	case *BashPPBasicLit:
		Walk(node.Value, f)
	case *BashPPIdent:
		Walk(node.Name, f)
	case *BashPPParenExpr:
		Walk(node.X, f)
	case *BashPPUnaryExpr:
		Walk(node.Op, f)
		Walk(node.X, f)
	case *BashPPAddressExpr:
		Walk(node.X, f)
	case *BashPPDerefExpr:
		Walk(node.X, f)
	case *BashPPNewExpr:
		Walk(node.New, f)
		Walk(node.AllocType, f)
		walkNilable(node.Init, f)
	case *BashPPTypeAssertExpr:
		Walk(node.X, f)
		walkNilable(node.Assert, f)
		walkNilable(node.TypeToken, f)
	case *BashPPBinaryExpr:
		Walk(node.X, f)
		Walk(node.Op, f)
		Walk(node.Y, f)
	case *BashPPConvertExpr:
		Walk(node.ConvType, f)
		walkNilable(node.ConvTypeExpr, f)
		Walk(node.X, f)
	case *BashPPIndexExpr:
		Walk(node.X, f)
		Walk(node.Index, f)
	case *BashPPSliceExpr:
		Walk(node.X, f)
		walkNilable(node.Low, f)
		walkNilable(node.High, f)
		walkNilable(node.Max, f)
	case *BashPPSelectorExpr:
		walkNilable(node.FuncType, f)
		Walk(node.X, f)
		Walk(node.Sel, f)
	case *BashPPFuncType:
		walkList(node.Params, f)
		walkList(node.Results, f)
	case *BashPPNamedType:
		Walk(node.Name, f)
		walkList(node.TypeArgs, f)
	case *BashPPTypeParamType:
		Walk(node.Name, f)
	case *BashPPUnionType:
		for _, term := range node.Terms {
			Walk(term, f)
		}
		walkList(node.Bars, f)
	case *BashPPApproxType:
		Walk(node.Term, f)
	case *BashPPCollectionType:
		walkNilable(node.Length, f)
		walkNilable(node.Key, f)
		walkNilable(node.Element, f)
	case *BashPPStructType:
		Walk(node.Struct, f)
		walkList(node.Fields, f)
	case *BashPPPointerType:
		Walk(node.Element, f)
	case *BashPPInterfaceType:
		Walk(node.Interface, f)
		if len(node.Elems) > 0 {
			walkList(node.Elems, f)
		} else {
			walkList(node.Methods, f)
		}
	case *BashPPInterfaceElem:
		walkNilable(node.Method, f)
		walkNilable(node.Embedded, f)
	case *BashPPMethodSpec:
		Walk(node.Name, f)
		walkList(node.Params, f)
		walkList(node.Results, f)
	case *BashPPCompositeLit:
		walkNilable(node.LitType, f)
		walkList(node.Elems, f)
	case *BashPPCompositeElem:
		walkNilable(node.Key, f)
		Walk(node.Value, f)
	case *BashPPCall:
		walkNilable(node.CalleeExpr, f)
		if node.FuncLit != nil {
			Walk(node.FuncLit, f)
		}
		walkList(node.Fun, f)
		walkList(node.TypeArgs, f)
		walkList(node.ArgNames, f)
		walkNilable(node.ArgType, f)
		walkNilable(node.ResultFuncType, f)
		for _, result := range node.ResultTypes {
			walkNilable(result, f)
		}
		if node.ArgExprs != nil {
			for _, arg := range node.ArgExprs {
				walkNilable(arg, f)
			}
		} else {
			walkList(node.Args, f)
		}
	case *BashPPCommandCall:
		walkList(node.Before, f)
		Walk(node.Call, f)
	case *BashPPImport:
		Walk(node.Kw, f)
		walkNilable(node.Language, f)
		walkNilable(node.Environment, f)
		walkNilable(node.As, f)
		walkNilable(node.Alias, f)
		walkNilable(node.Path, f)
		walkComments(node.Comments, f)
		walkList(node.Specs, f)
		walkComments(node.Last, f)
	case *BashPPImportSpec:
		walkComments(node.Comments, f)
		walkNilable(node.Alias, f)
		Walk(node.Path, f)
	case *BashPPFuncDecl:
		walkList(node.Decorators, f)
		walkNilable(node.Agentic, f)
		walkNilable(node.Kw, f)
		walkNilable(node.Name, f)
		if node.Receiver != nil {
			Walk(node.Receiver, f)
		}
		walkList(node.TypeParams, f)
		for _, field := range node.Params {
			Walk(field, f)
		}
		for _, field := range node.Results {
			Walk(field, f)
		}
		if node.Body != nil {
			Walk(node.Body, f)
		}
	case *BashPPFuncLit:
		walkNilable(node.Kw, f)
		for _, field := range node.Params {
			Walk(field, f)
		}
		for _, field := range node.Results {
			Walk(field, f)
		}
		if node.Body != nil {
			Walk(node.Body, f)
		}
	case *BashPPAgenticBlock:
		walkNilable(node.Kw, f)
		if node.Body != nil {
			Walk(node.Body, f)
		}
	case *BashPPDecorator:
		walkNilable(node.Name, f)
		walkList(node.Args, f)
		walkList(node.ArgNames, f)
		walkComments(node.Comments, f)
	case *BashPPField:
		walkNilable(node.Tag, f)
		walkList(node.Names, f)
		walkNilable(node.FieldType, f)
		walkNilable(node.FieldTypeExpr, f)
		walkNilable(node.Default, f)
	case *BashPPTypeParam:
		walkList(node.Names, f)
		walkNilable(node.Constraint, f)
	case *BashPPTypeArg:
		walkNilable(node.ArgType, f)
	case *BashPPReceiver:
		walkNilable(node.Name, f)
		walkNilable(node.RecvType, f)
		walkList(node.TypeParams, f)
	case *BashPPReturn:
		walkList(node.ResultExprs, f)
		walkNilable(node.Expr, f)
		walkNilable(node.Call, f)
		walkNilable(node.Kw, f)
		walkList(node.Results, f)
		if node.FuncLit != nil {
			Walk(node.FuncLit, f)
		}
	case *BashPPDefer:
		walkNilable(node.Kw, f)
		if node.Call != nil {
			Walk(node.Call, f)
		}
	case *BashPPGo:
		walkNilable(node.Kw, f)
		walkNilable(node.Call, f)
	case *BashPPChanType:
		if node.Element != nil {
			Walk(node.Element, f)
		} else {
			walkNilable(node.Elem, f)
		}
	case *BashPPMakeChan:
		walkNilable(node.Make, f)
		walkNilable(node.ChanType, f)
		if node.CapacityExpr != nil {
			Walk(node.CapacityExpr, f)
		} else {
			walkNilable(node.Capacity, f)
		}
	case *BashPPSend:
		if node.ChanExpr != nil {
			Walk(node.ChanExpr, f)
		} else {
			walkNilable(node.Chan, f)
		}
		if node.ValueExpr != nil {
			Walk(node.ValueExpr, f)
		} else {
			walkNilable(node.Value, f)
		}
	case *BashPPReceive:
		if node.ChanExpr != nil {
			Walk(node.ChanExpr, f)
		} else {
			walkNilable(node.Chan, f)
		}
	case *BashPPClose:
		walkNilable(node.Kw, f)
		walkNilable(node.Chan, f)
	case *BashPPSelect:
		walkList(node.Cases, f)
	case *BashPPSelectCase:
		walkNilable(node.Comm, f)
		walkComments(node.Comments, f)
		walkList(node.Stmts, f)
		walkComments(node.Last, f)
	case *BashPPRange:
		walkList(node.Names, f)
		walkNilable(node.Chan, f)
		walkNilable(node.Expr, f)
		walkNilable(node.Call, f)
		walkNilable(node.Body, f)
	default:
		panic(fmt.Sprintf("syntax.Walk: unexpected node type %T", node))
	}

	f(nil)
}

type nilableNode interface {
	Node
	comparable // pointer nodes, which can be compared to nil
}

func walkNilable[N nilableNode](node N, f func(Node) bool) {
	var zero N // nil
	if node != zero {
		Walk(node, f)
	}
}

func walkList[N Node](list []N, f func(Node) bool) {
	for _, node := range list {
		Walk(node, f)
	}
}

func walkComments(list []Comment, f func(Node) bool) {
	// Note that []Comment does not satisfy the generic constraint []Node.
	for i := range list {
		Walk(&list[i], f)
	}
}

// DebugPrint prints the provided syntax tree, spanning multiple lines and with
// indentation. Can be useful to investigate the content of a syntax tree.
func DebugPrint(w io.Writer, node Node) error {
	p := debugPrinter{out: w}
	p.print(reflect.ValueOf(node))
	p.printf("\n")
	return p.err
}

type debugPrinter struct {
	out   io.Writer
	level int
	err   error
}

func (p *debugPrinter) printf(format string, args ...any) {
	_, err := fmt.Fprintf(p.out, format, args...)
	if err != nil && p.err == nil {
		p.err = err
	}
}

func (p *debugPrinter) newline() {
	p.printf("\n")
	for range p.level {
		p.printf(".  ")
	}
}

func (p *debugPrinter) print(x reflect.Value) {
	switch x.Kind() {
	case reflect.Interface:
		if x.IsNil() {
			p.printf("nil")
			return
		}
		p.print(x.Elem())
	case reflect.Pointer:
		if x.IsNil() {
			p.printf("nil")
			return
		}
		p.printf("*")
		p.print(x.Elem())
	case reflect.Slice:
		p.printf("%s (len = %d) {", x.Type(), x.Len())
		if x.Len() > 0 {
			p.level++
			p.newline()
			for i := range x.Len() {
				p.printf("%d: ", i)
				p.print(x.Index(i))
				if i == x.Len()-1 {
					p.level--
				}
				p.newline()
			}
		}
		p.printf("}")

	case reflect.Struct:
		if v, ok := x.Interface().(Pos); ok {
			if v.IsRecovered() {
				p.printf("<recovered>")
				return
			}
			p.printf("%v:%v", v.Line(), v.Col())
			return
		}
		t := x.Type()
		p.printf("%s {", t)
		p.level++
		p.newline()
		for i := range t.NumField() {
			p.printf("%s: ", t.Field(i).Name)
			p.print(x.Field(i))
			if i == x.NumField()-1 {
				p.level--
			}
			p.newline()
		}
		p.printf("}")
	default:
		if s, ok := x.Interface().(fmt.Stringer); ok && !x.IsZero() {
			p.printf("%#v (%s)", x.Interface(), s)
		} else {
			p.printf("%#v", x.Interface())
		}
	}
}
