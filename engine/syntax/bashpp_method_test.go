// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
)

func TestBashPPMethodGrammarRoundTrip(t *testing.T) {
	t.Parallel()
	const src = "func (v Count) Value(prefix string) int {\n\treturn v\n}\nfunc (p *Count) Pointer() {\n\treturn\n}\n(*Count).Pointer(p)\n"
	for _, rd := range []io.Reader{strings.NewReader(src), funcOneByteReader{strings.NewReader(src)}} {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "method.bpp")
		if err != nil {
			t.Fatal(err)
		}
		value := f.Stmts[0].Cmd.(*BashPPFuncDecl)
		if value.Receiver == nil || value.Receiver.Pointer || value.Receiver.Name.Value != "v" || value.Receiver.RecvType.Value != "Count" {
			t.Fatalf("value receiver = %#v", value.Receiver)
		}
		pointer := f.Stmts[1].Cmd.(*BashPPFuncDecl)
		if pointer.Receiver == nil || !pointer.Receiver.Pointer || pointer.Receiver.RecvType.Value != "Count" {
			t.Fatalf("pointer receiver = %#v", pointer.Receiver)
		}
		expr := f.Stmts[2].Cmd.(*BashPPCall)
		if !expr.PointerMethodExpr || len(expr.Fun) != 2 || expr.Pos().Offset() != uint(strings.Index(src, "(*Count)")) {
			t.Fatalf("pointer method expression = %#v", expr)
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, f); err != nil || out.String() != src {
			t.Fatalf("print = %q, %v", out.String(), err)
		}
		seen := 0
		Walk(f, func(n Node) bool {
			if _, ok := n.(*BashPPReceiver); ok {
				seen++
			}
			return true
		})
		if seen != 2 {
			t.Fatalf("walk saw %d receivers", seen)
		}
	}
}

func TestBashPPMethodGrammarDiagnosticsAndIsolation(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"func () M() { return; }\n",
		"func (a, b T) M() { return; }\n",
		"func (r **T) M() { return; }\n",
	} {
		if _, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), ""); err == nil {
			t.Errorf("parsed malformed receiver %q", src)
		}
	}
	const valid = "func (v Count) M() {\n return\n}\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(valid), ""); err == nil {
			t.Errorf("classic %v accepted method declaration", lang)
		}
	}
}

func TestBashPPTypedReceiverValuesAndMethodValueAST(t *testing.T) {
	t.Parallel()
	const src = "var v Count = 7\nvar p *Count\nf := v.Show\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	v := f.Stmts[0].Cmd.(*BashPPDecl)
	p := f.Stmts[1].Cmd.(*BashPPDecl)
	mv := f.Stmts[2].Cmd.(*BashPPShortDecl)
	if v.DeclType.Value != "Count" || p.DeclType.Value != "*Count" || len(mv.MethodValue) != 2 {
		t.Fatalf("typed declarations/method value = %#v %#v %#v", v, p, mv)
	}
}

func TestBashPPGenericReceiverGrammarRoundTrip(t *testing.T) {
	t.Parallel()
	const src = "func (b *Box[T, U]) Get(v T) U {\n\treturn b.Value\n}\n"
	for _, rd := range []io.Reader{strings.NewReader(src), funcOneByteReader{strings.NewReader(src)}} {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "generic-method.bpp")
		if err != nil {
			t.Fatal(err)
		}
		decl := f.Stmts[0].Cmd.(*BashPPFuncDecl)
		if decl.Receiver == nil || !decl.Receiver.Pointer || len(decl.Receiver.TypeParams) != 2 || decl.Receiver.TypeParams[0].Value != "T" || decl.Receiver.TypeParams[1].Value != "U" {
			t.Fatalf("receiver = %#v", decl.Receiver)
		}
		if _, ok := decl.Params[0].FieldTypeExpr.(*BashPPTypeParamType); !ok {
			t.Fatalf("parameter type = %T, want receiver type parameter", decl.Params[0].FieldTypeExpr)
		}
		if _, ok := decl.Results[0].FieldTypeExpr.(*BashPPTypeParamType); !ok {
			t.Fatalf("result type = %T, want receiver type parameter", decl.Results[0].FieldTypeExpr)
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, f); err != nil || out.String() != src {
			t.Fatalf("print = %q, %v", out.String(), err)
		}
	}
}

// TestBashPPIndependentMethodTypeParams covers the Go 1.27 rule that a method
// may declare type parameters of its own, independent of the receiver's. The
// oracle is the real compiler: `func (r R) M[T any](v T) T` builds and runs
// under go1.27, so the dialect parses it rather than rejecting it.
func TestBashPPIndependentMethodTypeParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		src         string
		params      int
		recvParams  int
		paramIsTP   bool
		resultIsTP  bool
		constraints []string
	}{
		{
			name:        "ordinary receiver",
			src:         "func (r R) M[T any](v T) T {\n\treturn v\n}\n",
			params:      1,
			paramIsTP:   true,
			resultIsTP:  true,
			constraints: []string{"any"},
		},
		{
			name:        "constrained method parameter",
			src:         "func (r R) Sum[T Num](a T, b T) T {\n\treturn a\n}\n",
			params:      1,
			paramIsTP:   true,
			resultIsTP:  true,
			constraints: []string{"Num"},
		},
		{
			name:        "generic receiver and method scopes coexist",
			src:         "func (b Box[T]) Get[U any](v U) U {\n\treturn v\n}\n",
			params:      1,
			recvParams:  1,
			paramIsTP:   true,
			resultIsTP:  true,
			constraints: []string{"any"},
		},
		{
			name:        "pointer generic receiver with two method parameters",
			src:         "func (b *Box[T]) Pair[U any, V any](u U, v V) T {\n\treturn b.Value\n}\n",
			params:      2,
			recvParams:  1,
			paramIsTP:   true,
			resultIsTP:  true,
			constraints: []string{"any", "any"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Both readers: the whole-buffer one and the one-byte reader that
			// forces the streaming parser through every refill boundary.
			for _, rd := range []io.Reader{strings.NewReader(tc.src), funcOneByteReader{strings.NewReader(tc.src)}} {
				f, err := NewParser(Variant(LangBashPP)).Parse(rd, "generic-method.bpp")
				if err != nil {
					t.Fatal(err)
				}
				decl := f.Stmts[0].Cmd.(*BashPPFuncDecl)
				if decl.Receiver == nil || len(decl.Receiver.TypeParams) != tc.recvParams {
					t.Fatalf("receiver = %#v", decl.Receiver)
				}
				if got := bashppTypeParamNameCount(decl.TypeParams); got != tc.params {
					t.Fatalf("method type parameters = %d, want %d", got, tc.params)
				}
				var constraints []string
				for _, group := range decl.TypeParams {
					for range group.Names {
						named, ok := group.Constraint.(*BashPPNamedType)
						if !ok {
							t.Fatalf("constraint = %T", group.Constraint)
						}
						constraints = append(constraints, named.Name.Value)
					}
				}
				if len(constraints) != len(tc.constraints) {
					t.Fatalf("constraints = %v, want %v", constraints, tc.constraints)
				}
				for i, want := range tc.constraints {
					if constraints[i] != want {
						t.Fatalf("constraint %d = %q, want %q", i, constraints[i], want)
					}
				}
				if _, ok := decl.Params[0].FieldTypeExpr.(*BashPPTypeParamType); ok != tc.paramIsTP {
					t.Fatalf("parameter type = %T", decl.Params[0].FieldTypeExpr)
				}
				if _, ok := decl.Results[0].FieldTypeExpr.(*BashPPTypeParamType); ok != tc.resultIsTP {
					t.Fatalf("result type = %T", decl.Results[0].FieldTypeExpr)
				}
				// Positions must survive: the printer reconstructs the source
				// from the node alone, and Walk must reach both scopes.
				var out strings.Builder
				if err := NewPrinter().Print(&out, f); err != nil || out.String() != tc.src {
					t.Fatalf("print = %q, %v", out.String(), err)
				}
				var typeParams, receivers int
				Walk(f, func(n Node) bool {
					switch n.(type) {
					case *BashPPTypeParam:
						typeParams++
					case *BashPPReceiver:
						receivers++
					}
					return true
				})
				if receivers != 1 || typeParams != len(decl.TypeParams) {
					t.Fatalf("walk saw %d receivers and %d type parameters", receivers, typeParams)
				}
			}
		})
	}
}

func bashppTypeParamNameCount(params []*BashPPTypeParam) int {
	var n int
	for _, group := range params {
		n += len(group.Names)
	}
	return n
}

// TestBashPPIndependentMethodTypeParamDiagnostics keeps the shapes Go 1.27
// still rejects rejected, and keeps the whole method surface out of the
// classic language variants.
func TestBashPPIndependentMethodTypeParamDiagnostics(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		// `T redeclared in this block` from the real compiler: the receiver
		// and the method parameter share one scope.
		"func (b Box[T]) Get[T any](v T) T { return v; }\n",
		// A name list is not a signature.
		"func (b Box[T]) Get any](v T) T { return v; }\n",
		"func (b Box[T]) Get[U any] (v U) U { return v; }\n",
		"func (b Box[T]) [U any](v U) U { return v; }\n",
	} {
		if _, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), ""); err == nil {
			t.Errorf("parsed %q", src)
		}
	}
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, src := range []string{
			"func (b Box[T]) Get() { return; }\n",
			"func (b Box[T]) Get[U any](v U) U { return v; }\n",
		} {
			if f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), ""); err == nil {
				t.Errorf("classic %v accepted %q: %#v", lang, src, f)
			}
		}
	}
}
