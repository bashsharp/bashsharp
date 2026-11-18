// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"weak"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Instantiations of imported generic functions.
//
// The dependency helper registers an imported package's exported functions
// by name (`maps.Clone`), which has no meaning for a generic function: a
// generic function is not a value until it is instantiated, so the helper
// registered nothing and every call of one failed as an unknown symbol. The
// front end records each call's type arguments — spelled by the program or
// inferred by the checker — on the call, and the instantiation closure
// (bashpp_sprint165_runtime_instantiations.go) substitutes an enclosing
// generic body's bindings into them, so the concrete instantiations the
// program reaches are known before the helper is built. Each is registered
// under its instantiated spelling, and the call names that spelling.

// bashPPImportedInstance is one concrete instantiation of an imported
// generic function, rendered for the helper: Key is the symbol the call
// names, Selector the imported function (`alias.Name`) and Args the type
// arguments as Go source with the program's import aliases. refs names the
// original local types the arguments mention; host-only.
type bashPPImportedInstance struct {
	Key      string
	Selector string
	Args     []string
	refs     map[string]bool
}

// bashPPImportedInstanceKey is the symbol an instantiated imported generic
// function is registered under and called by: the selector followed by the
// instance suffix.
func bashPPImportedInstanceKey(selector string, args []syntax.BashPPTypeExpr) string {
	return selector + bashPPImportedInstanceSuffix(args)
}

// bashPPImportedInstanceSuffix is the interpreter's spelling of an
// instantiation's type arguments, `[T1, T2]`; a call carries it beside its
// selector (bashPPBridgeRequest.Instance) so the selector itself stays the
// name every host-side policy is keyed on.
func bashPPImportedInstanceSuffix(args []syntax.BashPPTypeExpr) string {
	return "[" + bashPPInstantiationArgsText(args) + "]"
}

// bashPPImportedInstances renders every reached instantiation of an imported
// generic function whose type arguments are expressible in the helper. An
// instantiation with an inexpressible argument is not registered, and its
// call fails as before.
func (r *Runner) bashPPImportedInstances() []bashPPImportedInstance {
	reached := r.bashPPReachedImportedInstantiations()
	if len(reached) == 0 {
		return nil
	}
	local := r.bashPPLocalTypeRenderer()
	keys := make([]string, 0, len(reached))
	for key := range reached {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []bashPPImportedInstance
	for _, key := range keys {
		item := reached[key]
		local.refs = map[string]bool{}
		args := make([]string, len(item.args))
		expressible := true
		for i, arg := range item.args {
			rendered, ok := local.source(arg, 0)
			if !ok {
				expressible = false
				break
			}
			args[i] = rendered
		}
		if !expressible {
			continue
		}
		out = append(out, bashPPImportedInstance{Key: key, Selector: item.alias + "." + item.name, Args: args, refs: local.refs})
	}
	return out
}

// bashPPLocalTypeRenderer is the local type set bashPPLocalTypeDescriptors
// renders declarations with, built from the same declarations under the
// same ambiguity rule, for rendering a type expression outside a declaration.
func (r *Runner) bashPPLocalTypeRenderer() *bashPPLocalTypeSet {
	cache := r.bashPPLocalTypeDeclarationIndex()
	// A renderer carries request-local refs and substitution state, so only
	// the immutable declaration index is shared. In particular, use the
	// Runner's current import map rather than pinning the map from the scan.
	return &bashPPLocalTypeSet{declared: cache.declared, imports: r.bashPPImports, generics: cache.generics}
}

// bashPPLocalTypeDeclCache is the immutable result of scanning one source file
// for declarations, lexical scopes and bridge metadata candidates. Go-source
// execution does not rewrite its syntax tree: generic body substitution clones
// nodes before changing them. File identity therefore invalidates the scan
// while allowing copied Runners to share a hit.
type bashPPLocalTypeDeclCache struct {
	file               *syntax.File
	declared           map[string]syntax.BashPPTypeExpr
	generics           map[string]*syntax.BashPPDecl
	typeDecls          []*syntax.BashPPDecl
	topDecls           map[*syntax.BashPPDecl]bool
	inGeneric          map[*syntax.BashPPDecl]bool
	packageGenerics    map[string]*syntax.BashPPDecl
	methods            map[string][]*syntax.BashPPFuncDecl
	spelled            []*syntax.BashPPNamedType
	anonymous          []syntax.BashPPTypeExpr
	genericBridgeTypes []*syntax.BashPPNamedType
	selectorRefs       [][2]string
	reservedNames      []string
	reservedDecls      map[string]*syntax.BashPPDecl
	localTypes         *goSourceLocalTypeIndex
	// fullScans is a scan-count instrument. A non-nil file is traversed once,
	// regardless of how many descriptor and metadata consumers use the index.
	fullScans int
}

type bashPPLocalTypeDeclCacheEntry struct {
	mu    sync.Mutex
	index weak.Pointer[bashPPLocalTypeDeclCache]
}

var bashPPLocalTypeDeclCaches sync.Map // weak.Pointer[syntax.File] -> *bashPPLocalTypeDeclCacheEntry

func (r *Runner) bashPPLocalTypeDeclarationIndex() *bashPPLocalTypeDeclCache {
	cache := r.bashPPTools.localTypeDecls
	if cache == nil || cache.file != r.bashPPGoSourceFile {
		cache = bashPPScanLocalTypeDecls(r.bashPPGoSourceFile)
		r.bashPPTools.localTypeDecls = cache
	}
	return cache
}

func bashPPScanLocalTypeDecls(file *syntax.File) *bashPPLocalTypeDeclCache {
	if file == nil {
		return bashPPBuildLocalTypeDeclCache(nil)
	}
	key := weak.Make(file)
	value, loaded := bashPPLocalTypeDeclCaches.LoadOrStore(key, new(bashPPLocalTypeDeclCacheEntry))
	if !loaded {
		runtime.AddCleanup(file, func(key weak.Pointer[syntax.File]) {
			bashPPLocalTypeDeclCaches.Delete(key)
		}, key)
	}
	entry := value.(*bashPPLocalTypeDeclCacheEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if index := entry.index.Value(); index != nil {
		return index
	}
	index := bashPPBuildLocalTypeDeclCache(file)
	entry.index = weak.Make(index)
	return index
}

func bashPPBuildLocalTypeDeclCache(file *syntax.File) *bashPPLocalTypeDeclCache {
	declared := map[string]syntax.BashPPTypeExpr{}
	generics := map[string]*syntax.BashPPDecl{}
	ambiguous := map[string]bool{}
	cache := &bashPPLocalTypeDeclCache{
		file: file, declared: declared, generics: generics,
		topDecls: map[*syntax.BashPPDecl]bool{}, inGeneric: map[*syntax.BashPPDecl]bool{},
		packageGenerics: map[string]*syntax.BashPPDecl{}, methods: map[string][]*syntax.BashPPFuncDecl{},
		reservedDecls: map[string]*syntax.BashPPDecl{},
	}
	if file == nil {
		cache.localTypes = &goSourceLocalTypeIndex{decls: make(map[string][]goSourceLocalTypeDecl)}
		return cache
	}
	genericTop := map[*syntax.Stmt]bool{}
	for _, stmt := range file.Stmts {
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			cache.topDecls[d] = true
			if d.Site == syntax.StartTypeDecl && d.Name != nil && len(d.TypeParams) > 0 && !d.Alias {
				cache.packageGenerics[d.Name.Value] = d
			}
			if d.Site == syntax.StartTypeDecl && d.Name != nil && bashPPHelperReserved[d.Name.Value] && len(d.TypeParams) == 0 && !d.Alias && d.DeclTypeExpr != nil {
				if _, dup := cache.reservedDecls[d.Name.Value]; !dup {
					cache.reservedNames = append(cache.reservedNames, d.Name.Value)
				}
				cache.reservedDecls[d.Name.Value] = d
			}
		case *syntax.BashPPFuncDecl:
			genericTop[stmt] = len(d.TypeParams) > 0 || d.Receiver != nil && len(d.Receiver.TypeParams) > 0
			if d.Receiver != nil && d.Receiver.RecvType != nil && d.Name != nil && d.Name.Value != "_" {
				owner := d.Receiver.RecvType.Value
				cache.methods[owner] = append(cache.methods[owner], d)
			}
		}
	}
	sort.Strings(cache.reservedNames)
	typeParams := map[string]bool{"_": true}
	stmtsByTop := make(map[*syntax.Stmt][]*syntax.Stmt, len(file.Stmts))
	cache.fullScans = 1
	for _, top := range file.Stmts {
		syntax.Walk(top, func(node syntax.Node) bool {
			if stmt, ok := node.(*syntax.Stmt); ok {
				stmtsByTop[top] = append(stmtsByTop[top], stmt)
			}
			d, ok := node.(*syntax.BashPPDecl)
			if ok && d.Site == syntax.StartTypeDecl && d.DeclTypeExpr != nil && d.Name != nil {
				cache.typeDecls = append(cache.typeDecls, d)
				if genericTop[top] {
					cache.inGeneric[d] = true
				}
				if len(d.TypeParams) == 0 {
					if _, exists := declared[d.Name.Value]; exists {
						ambiguous[d.Name.Value] = true
					}
					declared[d.Name.Value] = d.DeclTypeExpr
				} else if !d.Alias {
					if _, exists := generics[d.Name.Value]; exists {
						ambiguous[d.Name.Value] = true
					}
					generics[d.Name.Value] = d
				}
			}
			switch n := node.(type) {
			case *syntax.BashPPNamedType:
				if n.Name != nil && len(n.TypeArgs) > 0 {
					cache.spelled = append(cache.spelled, n)
				}
			case *syntax.BashPPStructType:
				cache.anonymous = append(cache.anonymous, n)
			case *syntax.BashPPInterfaceType:
				if len(n.Methods) > 0 {
					cache.anonymous = append(cache.anonymous, n)
				}
			case *syntax.BashPPReceiver:
				for _, param := range n.TypeParams {
					typeParams[param.Value] = true
				}
			case *syntax.BashPPTypeParam:
				for _, name := range n.Names {
					typeParams[name.Value] = true
				}
			case *syntax.BashPPCall:
				if len(n.Fun) >= 2 {
					cache.selectorRefs = append(cache.selectorRefs, [2]string{n.Fun[0].Value, n.Fun[1].Value})
				}
			case *syntax.BashPPSelectorExpr:
				if id, ok := n.X.(*syntax.BashPPIdent); ok && id.Name != nil && n.Sel != nil {
					cache.selectorRefs = append(cache.selectorRefs, [2]string{id.Name.Value, n.Sel.Value})
				}
			}
			return true
		})
	}
	for _, named := range cache.spelled {
		if !bashPPTypeExprMentionsNames(named, typeParams) {
			cache.genericBridgeTypes = append(cache.genericBridgeTypes, named)
		}
	}
	for name := range ambiguous {
		delete(declared, name)
		delete(generics, name)
	}
	cache.localTypes = goSourceBuildLocalTypeIndex(file, stmtsByTop)
	return cache
}

// bashPPImportedInstanceIdentity is the session identity contribution of the
// registered instantiations.
func bashPPImportedInstanceIdentity(list []bashPPImportedInstance) string {
	data, _ := json.Marshal(list)
	return string(data)
}

// bashPPImportedInstanceSymbols emits the helper's symbol entries for the
// instantiations of one imported generic function: name in the package
// imported under helperAlias, with typeParams type parameters. keyNames are
// the program's aliases for the package and aliases maps them to the
// helper's. An instantiation naming a local type the helper does not
// materialise is skipped; registering it would leave the helper unbuildable.
// It reports whether any entry was emitted.
func bashPPImportedInstanceSymbols(symbols *strings.Builder, list []bashPPImportedInstance, keyNames []string, name, helperAlias string, typeParams int, aliases map[string]string, materialised map[string]bool) (bool, error) {
	emitted := false
	for _, instance := range list {
		alias, symbol, ok := strings.Cut(instance.Selector, ".")
		if !ok || symbol != name || len(instance.Args) != typeParams {
			continue
		}
		bound := false
		for _, key := range keyNames {
			if key == alias {
				bound = true
			}
		}
		if !bound {
			continue
		}
		expressible := true
		for ref := range instance.refs {
			if !materialised[ref] {
				expressible = false
			}
		}
		if !expressible {
			continue
		}
		args := make([]string, len(instance.Args))
		for i, arg := range instance.Args {
			// A type argument qualified by a name the helper does not
			// import is not expressible in it; registering the
			// instantiation would leave the helper unbuildable.
			if !bashPPTypeImportsKnown(arg, aliases) {
				expressible = false
				break
			}
			mapped, err := bashPPNativeTypeImports(arg, aliases)
			if err != nil {
				return false, err
			}
			args[i] = mapped
		}
		if !expressible {
			continue
		}
		symbols.WriteString(strconv.Quote(instance.Key) + ": reflect.ValueOf(" + helperAlias + "." + name + "[" + strings.Join(args, ", ") + "]),\n")
		emitted = true
	}
	return emitted, nil
}

// bashPPTypeImportsKnown reports whether every package-qualified name in
// the type expression decl is qualified by an alias the helper imports.
func bashPPTypeImportsKnown(decl string, aliases map[string]string) bool {
	expr, err := parser.ParseExpr(decl)
	if err != nil {
		return false
	}
	known := true
	ast.Inspect(expr, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] == "" {
				known = false
			}
		}
		return known
	})
	return known
}
