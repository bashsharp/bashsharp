// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The structural half of the aliased-cell discipline on the READER side.
//
// A cell taken straight out of [bashPPScope.lookup] is exactly the storage an
// interpreted `go` statement aliases: [bashPPCloner.cloneCell] hands the task
// the same *bashPPCell the launcher's scope holds. Reading several of its
// fields without [bashPPCell.view] is therefore a host data race, and for the
// interface-typed fields it is worse than an unspecified value — a torn
// interface word is a pointer into unrelated memory, which is how Sprint 281
// T4c's probe corpus ended up reporting races inside syntax nodes.
//
// T4c fixed every such reader its corpus reached and T4f the last of them,
// bashPPShortDecl. What is listed below is the one reader that reads such a
// cell on purpose, with the reason it may. The point of the scan is the
// RATCHET: a new function cannot read a looked-up cell's fields directly
// without either taking a view or being added here on purpose.
//
// The scan is deliberately syntactic and deliberately narrow:
//   - the guarded field set is read out of (*bashPPCell).storeFields, so it
//     cannot drift from the fields a writer publishes as a bundle;
//   - only identifiers assigned from a `lookup(…)` call count, because those
//     are the ones that certainly name storage a task can alias;
//   - an identifier anywhere assigned from `.view()` is clean, which is the
//     fix this lane applied;
//   - a store is not a read: writers answer to the guard, not to this scan.
//     That covers the WHOLE selector chain an assignment targets, so
//     `cell.vr.Str = text` is one store into cell.vr and not a read of it;
//   - an identifier whose guard the function both takes and releases reads
//     under that guard, which is the discipline's other half: a read-modify
//     -write of one binding — the attribute merge in [Runner.setVar], the
//     scalar stores in bashPPForAssign and bashPPIncDec — has to happen inside
//     a single region, and a snapshot cannot express it. The scan trusts such a
//     function as far as the guard reaches; what keeps it honest is the rule in
//     bashpp_cell_share.go that a guarded region moves fields and does nothing
//     else, which is short enough to check by eye at each of the six sites.
var bashPPCellDirectReaders = []string{
	// bashPPInvoke binds each parameter of the frame it is entering: the cell it
	// looks up was declared by this goroutine a few statements earlier, and no
	// `go` statement of the callee has run yet, so nothing can have aliased it.
	// Reading back the fields it just stored is a private read.
	// The parameter binding and result settling that bashPPInvoke did inline
	// moved into these helpers when its frame was shrunk for deep recursion
	// (Sprint 374); the direct reads moved with the code, unchanged.
	"bashPPBindParams",
	"bashPPSettleResultCells",
	// These typed-int fast paths reject cell.guard != nil before reading
	// any guarded field. They operate only on private cells and decline to
	// the general evaluator for shared storage. The syntactic scan cannot
	// infer that early-return condition; it is reviewed explicitly here.
	"bashPPFastIntIndex",
	"bashPPFastIntIndexAssign",
	"bashPPFastIntSelector",
}

// markStore marks an assignment target and every selector it is reached
// through as written. `cell.vr.Str = text` stores into cell.vr; the key of an
// indexed target is NOT on that chain and stays a read.
func markStore(writes map[ast.Node]bool, target ast.Expr) {
	for {
		writes[target] = true
		switch x := target.(type) {
		case *ast.SelectorExpr:
			target = x.X
		case *ast.IndexExpr:
			target = x.X
		case *ast.StarExpr:
			target = x.X
		case *ast.ParenExpr:
			target = x.X
		default:
			return
		}
	}
}

func bashPPCellPtrType(e ast.Expr) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "bashPPCell"
}

// bashPPGuardedCellFields is the field set (*bashPPCell).storeFields publishes.
func bashPPGuardedCellFields(t *testing.T, pkg *ast.Package) map[string]bool {
	t.Helper()
	guarded := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "storeFields" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok {
						guarded[sel.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	if len(guarded) < 10 {
		t.Fatalf("found %d guarded fields in (*bashPPCell).storeFields; the scan cannot have read it", len(guarded))
	}
	return guarded
}

func TestBashPPCellReadersTakeAView(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs["interp"]
	if pkg == nil {
		t.Fatal("package interp not parsed")
	}
	guarded := bashPPGuardedCellFields(t, pkg)

	found := map[string][]string{}
	for name, file := range pkg.Files {
		if strings.HasSuffix(name, "bashpp_cell_share.go") {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			looked, viewed := map[string]bool{}, map[string]bool{}
			locked, unlocked := map[string]bool{}, map[string]bool{}
			writes := map[ast.Node]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok {
							switch sel.Sel.Name {
							case "lock":
								locked[id.Name] = true
							case "unlock":
								unlocked[id.Name] = true
							}
						}
					}
				case *ast.UnaryExpr:
					// Taking an address is a handle to write through.
					if x.Op == token.AND {
						writes[x.X] = true
					}
				case *ast.IncDecStmt:
					markStore(writes, x.X)
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						markStore(writes, lhs)
					}
					if len(x.Lhs) != len(x.Rhs) {
						return true
					}
					for i, lhs := range x.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						call, ok := x.Rhs[i].(*ast.CallExpr)
						if !ok {
							continue
						}
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						switch sel.Sel.Name {
						case "view":
							viewed[id.Name] = true
						case "lookup":
							looked[id.Name] = true
						}
					}
				}
				return true
			})
			for name := range locked {
				if unlocked[name] {
					viewed[name] = true
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || writes[ast.Node(sel)] || !guarded[sel.Sel.Name] {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || !looked[id.Name] || viewed[id.Name] {
					return true
				}
				found[fn.Name.Name] = append(found[fn.Name.Name], fset.Position(sel.Pos()).String())
				return true
			})
		}
	}

	var added []string
	for name, where := range found {
		if !slices.Contains(bashPPCellDirectReaders, name) {
			added = append(added, name+" at "+where[0])
		}
	}
	sort.Strings(added)
	for _, where := range added {
		t.Errorf("READER-DIRECT %s: takes a looked-up cell's guarded fields straight off the cell. "+
			"Snapshot it with cell.view() (or viewVar/viewInterface for a single field), as "+
			"bashpp_cell_share.go describes. If the cell genuinely cannot be aliased into a task, "+
			"add the function to bashPPCellDirectReaders.", where)
	}

	var gone []string
	for _, name := range bashPPCellDirectReaders {
		if found[name] == nil {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	for _, name := range gone {
		t.Errorf("READER-STALE %s: no longer reads a looked-up cell directly; drop it from "+
			"bashPPCellDirectReaders so the list keeps naming real work", name)
	}
}
