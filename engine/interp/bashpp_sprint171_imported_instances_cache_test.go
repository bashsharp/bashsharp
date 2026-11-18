// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestLocalTypeRendererDeclarationCacheLifecycle(t *testing.T) {
	firstFile := bashPPParse(t, "type Local int\n")
	runner, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	runner.bashPPGoSourceFile = firstFile
	runner.bashPPImports = map[string]string{"dep": "first/path"}
	first := runner.bashPPLocalTypeRenderer()
	firstCache := runner.bashPPTools.localTypeDecls
	first.refs = map[string]bool{"request-only": true}

	// Reuse the immutable declaration scan, but never the mutable renderer or
	// an import namespace captured by an earlier bridge request.
	runner.bashPPImports = map[string]string{"dep": "second/path"}
	again := runner.bashPPLocalTypeRenderer()
	if runner.bashPPTools.localTypeDecls != firstCache {
		t.Fatal("unchanged source file rebuilt declaration scan")
	}
	if again == first || again.refs != nil {
		t.Fatal("mutable renderer state was cached")
	}
	if again.imports["dep"] != "second/path" {
		t.Fatalf("renderer imports = %#v", again.imports)
	}

	secondFile := bashPPParse(t, "type Replacement string\n")
	runner.bashPPGoSourceFile = secondFile
	changed := runner.bashPPLocalTypeRenderer()
	if runner.bashPPTools.localTypeDecls == firstCache {
		t.Fatal("replacement source file reused declaration scan")
	}
	if _, ok := changed.declared["Replacement"]; !ok {
		t.Fatalf("replacement declarations = %#v", changed.declared)
	}
	if _, stale := changed.declared["Local"]; stale {
		t.Fatalf("stale declarations = %#v", changed.declared)
	}

	// Reset preserves toolchain configuration. The source-file key still
	// prevents a later Run over another file from observing the old scan.
	runner.Reset()
	runner.bashPPGoSourceFile = firstFile
	afterReset := runner.bashPPLocalTypeRenderer()
	if _, ok := afterReset.declared["Local"]; !ok {
		t.Fatalf("post-Reset declarations = %#v", afterReset.declared)
	}
}

func TestLocalTypeRendererDeclarationCacheSemantics(t *testing.T) {
	file := bashPPParse(t, `
type Plain = int
type Defined[T any] struct { Value T }
type GenericAlias[T any] = Defined[T]
type Reused int
f() { type Reused string; }
`)
	runner, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	runner.Reset()
	runner.bashPPGoSourceFile = file
	renderer := runner.bashPPLocalTypeRenderer()

	if _, ok := renderer.declared["Plain"]; !ok {
		t.Fatal("non-generic alias disappeared from declaration scan")
	}
	if _, ok := renderer.generics["Defined"]; !ok {
		t.Fatal("defined generic disappeared from declaration scan")
	}
	if _, ok := renderer.generics["GenericAlias"]; ok {
		t.Fatal("generic alias acquired a defined-type identity")
	}
	if _, ok := renderer.declared["Reused"]; ok {
		t.Fatal("ambiguous local type name remained renderable")
	}

	// A child Runner may share the immutable scan. Each child still owns its
	// renderer and import map, so concurrent callback/subshell requests do not
	// share refs or other per-render state.
	const copies = 8
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		child := runner.Subshell()
		child.bashPPImports = map[string]string{"dep": "path"}
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := child.bashPPLocalTypeRenderer()
			if child.bashPPTools.localTypeDecls != runner.bashPPTools.localTypeDecls {
				t.Error("child Runner rebuilt immutable declaration scan")
			}
			local.refs = map[string]bool{"owned": true}
			if _, ok := local.source(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Plain"}}, 0); !ok {
				t.Error("child Runner could not render cached alias")
			}
		}()
	}
	wg.Wait()
}
