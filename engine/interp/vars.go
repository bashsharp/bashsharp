// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"maps"
	mathrand "math/rand/v2"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func newOverlayEnviron(parent expand.Environ, background bool) *overlayEnviron {
	oenv := &overlayEnviron{}
	if !background {
		oenv.parent = parent
	} else {
		// We could do better here if the parent is also an overlayEnviron;
		// measure with profiles or benchmarks before we choose to do so.
		for name, vr := range parent.Each {
			oenv.Set(name, vr)
		}
		if p, ok := parent.(*overlayEnviron); ok && p.goCommandTracking() {
			oenv.goCommandTouched = p.goCommandTouchedNames()
		}
	}
	return oenv
}

// overlayEnviron is our main implementation of [expand.WriteEnviron].
type overlayEnviron struct {
	// parent is non-nil if [values] is an overlay over a parent environment
	// which we can safely reuse without data races, such as non-background subshells
	// or function calls.
	parent expand.Environ
	// goCommandTouched records writes after shell startup. It is nil until
	// tracking begins, so synthesized startup variables are not guest input.
	goCommandTouched map[string]bool

	// values maps normalized variable names, per [overlayEnviron.normalize].
	values map[string]namedVariable

	// We need to know if the current scope is a function's scope, because
	// functions can modify global variables. When true, [parent] must not be nil.
	funcScope bool

	// funsubScope is set for bash 5.3 `${ cmd; }` / mksh `${|cmd;}` bodies.
	// Like funcScope it allows `return` and `local`; ordinary assignments
	// still write through to the caller, while local variables remain scoped
	// to the overlay. Implies funcScope.
	funsubScope bool

	// skip caches [overlayEnviron.holder] for a function scope that holds
	// nothing: the nearest ancestor that can answer a lookup or take a
	// write-through. A deep recursion is a chain of such empty scopes, and
	// without the cache every global read or write from the innermost frame
	// walks the whole chain, making the program quadratic in its call depth.
	// The cache is valid while skipGen matches [overlayLateTouch].
	skip    *overlayEnviron
	skipGen uint64

	// liveChildren counts the function scopes currently chained on this
	// one. A scope that first gains a value while a child is chained on it
	// invalidates every cached skip, since a child may have cached a jump
	// past it; see [overlayEnviron.touch].
	liveChildren int
}

// overlayLateTouch counts the times a function scope gained its first value
// while a child scope was chained on it. Cached skips are stamped with it and
// are recomputed when it moves.
var overlayLateTouch atomic.Uint64

// newFuncScopeEnviron chains a function scope on parent.
func newFuncScopeEnviron(parent expand.WriteEnviron, funsub bool) *overlayEnviron {
	if p, ok := parent.(*overlayEnviron); ok {
		p.liveChildren++
	}
	return &overlayEnviron{parent: parent, funcScope: true, funsubScope: funsub}
}

// release unchains a function scope from its parent when the frame that
// pushed it returns.
func (o *overlayEnviron) release() {
	if p, ok := o.parent.(*overlayEnviron); ok && o.funcScope {
		p.liveChildren--
	}
}

// touch prepares o to hold its first value.
func (o *overlayEnviron) touch() {
	if o.values != nil {
		return
	}
	o.values = make(map[string]namedVariable)
	if o.funcScope && o.liveChildren > 0 {
		overlayLateTouch.Add(1)
	}
}

// holder is the nearest scope, starting at o, that a lookup or a
// write-through has to consult: o itself when it holds a value, is not a
// function scope, or sits on a foreign environment; otherwise the holder of
// its parent, cached in skip.
func (o *overlayEnviron) holder() *overlayEnviron {
	if o.values != nil || !o.funcScope {
		return o
	}
	p, ok := o.parent.(*overlayEnviron)
	if !ok {
		return o
	}
	if gen := overlayLateTouch.Load(); o.skip == nil || o.skipGen != gen {
		o.skip, o.skipGen = p.holder(), gen
	}
	return o.skip
}

// next is the scope a lookup that missed in o continues at, or nil when o
// has no overlay parent.
func (o *overlayEnviron) next() *overlayEnviron {
	if p, ok := o.parent.(*overlayEnviron); ok {
		return p.holder()
	}
	return nil
}

// namedVariable records the original name of a variable for platforms
// where variable names are matched in a case-insensitive way.
type namedVariable struct {
	// TODO(v4): consider adding this field to [expand.Variable],
	// as a general way for a variable to report its original name.
	// This can be useful for GOOS=windows with case insensitive env vars,
	// as otherwise it's not possible to Environ.Get a var
	// and know what was its original name without looping over Environ.Each.
	Name string
	expand.Variable
	Temp bool
	Prev expand.Variable
}

// normalize is the identity: shell variable names are case-sensitive on
// every platform, Windows included. Only the OS environment is
// case-insensitive there, and that is handled once at the boundary — the
// process environment arrives through expand.ListEnviron with its names
// upper-cased, so PATH is found however Windows spelled it.
//
// Folding a name onto an inherited environment variable, which this used to
// do on Windows, makes a script's own `$a` resolve to an exported `A`:
// bash's varenv23.sub sets A=AVAR in the parent and expects the child's
// unset `$a` to stay unset (varenv.right:291).
func (o *overlayEnviron) normalize(name string) string { return name }

func (o *overlayEnviron) Get(name string) expand.Variable {
	if base, idx, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
		return environArrayElemAsScalar(idx, o.Get(base), o)
	}
	normalized := o.normalize(name)
	for cur := o; ; {
		if vr, ok := cur.values[normalized]; ok {
			return vr.Variable
		}
		next := cur.next()
		if next == nil {
			if cur.parent != nil {
				return cur.parent.Get(name)
			}
			return expand.Variable{}
		}
		cur = next
	}
}

func (o *overlayEnviron) ResolveNameRef(name string) expand.Variable {
	if base, idx, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
		return environArrayElemAsScalar(idx, o.ResolveNameRef(base), o)
	}
	normalized := o.normalize(name)
	if vr, ok := o.values[normalized]; ok {
		if vr.Local && vr.Kind == expand.NameRef && vr.Str == name && o.parent != nil {
			return o.parent.Get(name)
		}
		return vr.Variable
	}
	if o.parent != nil {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.ResolveNameRef(name)
		}
		return o.parent.Get(name)
	}
	return expand.Variable{}
}

func environArrayElemAsScalar(idx string, vr expand.Variable, env expand.Environ) expand.Variable {
	elem := expand.Variable{
		Kind:       expand.String,
		Integer:    vr.Integer,
		Upper:      vr.Upper,
		Lower:      vr.Lower,
		Capitalize: vr.Capitalize,
	}
	switch vr.Kind {
	case expand.Indexed:
		n, err := strconv.Atoi(idx)
		if err != nil {
			return elem
		}
		if n < 0 {
			n = indexedNegativeOffset(vr, n)
		}
		if vr.IndexedSet(n) {
			elem.Set = true
			elem.Str = vr.IndexedElem(n)
		}
	case expand.Associative:
		if vr.Map != nil {
			idx = environAssocSubscriptKey(idx, env)
			if s, ok := vr.Map[idx]; ok {
				elem.Set = true
				elem.Str = s
			}
		}
	default:
		if idx == "0" && vr.IsSet() {
			elem.Set = true
			elem.Str = vr.String()
		}
	}
	return elem
}

func environAssocSubscriptKey(idx string, env expand.Environ) string {
	expr := parseArrayTargetIndex(idx)
	word, ok := expr.(*syntax.Word)
	if !ok {
		return idx
	}
	key, ok := environAssocSubscriptWord(word, env)
	if !ok {
		return idx
	}
	return key
}

func environAssocSubscriptWord(word *syntax.Word, env expand.Environ) (string, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			s, ok := environAssocSubscriptParts(part.Parts, env)
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case *syntax.ParamExp:
			if part.Param == nil || part.NestedParam != nil || part.Index != nil ||
				part.Slice != nil || part.Repl != nil || part.Exp != nil ||
				part.Length || part.Width || part.Excl || part.Names != 0 {
				return "", false
			}
			b.WriteString(env.Get(part.Param.Value).String())
		default:
			return "", false
		}
	}
	return bashAssocAssignKey(b.String(), false), true
}

func environAssocSubscriptParts(parts []syntax.WordPart, env expand.Environ) (string, bool) {
	return environAssocSubscriptWord(&syntax.Word{Parts: parts}, env)
}

func (r *Runner) shellArgv0() string {
	switch {
	case r.argv0 != "":
		return r.argv0
	case r.filename != "":
		if r.opts[optPosix] && r.filename == "bash" && len(r.bashSource) > 0 {
			return "sh"
		}
		return r.filename
	default:
		if r.opts[optPosix] {
			return "sh"
		}
		return "bashy"
	}
}

func (o *overlayEnviron) Set(name string, vr expand.Variable) error {
	normalized := o.normalize(name)
	prev, inOverlay := o.values[normalized]
	// Manipulation of a global var inside a function or funsub. Local
	// variables stay in the overlay; ordinary assignments write through.
	if o.funcScope && !vr.Local && prev.Local && prev.Kind == expand.NameRef && prev.Str == name && o.parent != nil {
		return o.parent.(expand.WriteEnviron).Set(name, vr)
	}
	if o.funcScope && !vr.Local && !prev.Local {
		// In a function, the parent environment is ours, so it's always read-write.
		if next := o.next(); next != nil {
			return next.Set(name, vr)
		}
		return o.parent.(expand.WriteEnviron).Set(name, vr)
	}
	if !inOverlay && o.parent != nil {
		prev.Variable = o.parent.Get(name)
		// Bash only refuses `local` when it would shadow a readonly
		// *global* ("since I believe that this could be a security
		// hole" — variables.c); a readonly local belonging to a
		// calling function may be shadowed, and the shadow starts
		// fresh rather than inheriting the value or readonly bit.
		if vr.Local && prev.ReadOnly && prev.Local {
			prev.Variable = expand.Variable{}
			vr.ReadOnly = false
		}
	}

	o.touch()
	parentTemp := false
	if !inOverlay && o.parent != nil {
		if p, ok := o.parent.(*overlayEnviron); ok {
			parentTemp = p.isTemp(name)
		}
	}
	if vr.Kind == expand.KeepValue {
		vr.Set = prev.Set
		vr.Kind = prev.Kind
		vr.Str = prev.Str
		vr.List = prev.List
		vr.ListSet = prev.ListSet
		vr.Map = prev.Map
	} else if prev.ReadOnly && prev.Declared() {
		if prev.Kind == expand.NameRef && vr.Kind == expand.NameRef {
			vr.ReadOnly = true
			o.markGoCommandTouched(name)
			goto store
		}
		if !(prev.Kind == expand.NameRef && prev.Str == "" && vr.Kind == expand.String && vr.ReadOnly) {
			return fmt.Errorf("readonly variable")
		}
	}
	o.markGoCommandTouched(name)
	if !vr.IsSet() { // unsetting
		// Preserve the variable when it carries an attribute that
		// must outlive the assignment — local, integer, exported,
		// readonly, case-conversion — so `declare -u foo` (no
		// value) stays declared.
		if prev.Local || vr.Local || vr.Exported || vr.ReadOnly ||
			vr.Upper || vr.Lower || vr.Capitalize {
			vr.Local = prev.Local || vr.Local
			temp := prev.Temp || parentTemp
			if vr.Local && !inOverlay {
				temp = prev.Temp
			}
			o.values[normalized] = namedVariable{Name: name, Variable: vr, Temp: temp, Prev: prev.Variable}
			return nil
		}
		delete(o.values, normalized)
	}
	// modifying the entire variable
store:
	vr.Local = prev.Local || vr.Local
	temp := prev.Temp || parentTemp
	if vr.Local && !inOverlay {
		temp = prev.Temp
	}
	o.values[normalized] = namedVariable{Name: name, Variable: vr, Temp: temp, Prev: prev.Prev}
	return nil
}

func (o *overlayEnviron) markGoCommandTouched(name string) {
	if o.goCommandTouched == nil {
		if p, ok := o.parent.(*overlayEnviron); ok && p.goCommandTracking() {
			o.goCommandTouched = make(map[string]bool)
		}
	}
	if o.goCommandTouched != nil {
		o.goCommandTouched[name] = true
	}
}

func (o *overlayEnviron) goCommandTracking() bool {
	if o.goCommandTouched != nil {
		return true
	}
	p, ok := o.parent.(*overlayEnviron)
	return ok && p.goCommandTracking()
}

func (o *overlayEnviron) goCommandTouchedNames() map[string]bool {
	names := make(map[string]bool)
	for current := o; current != nil; {
		for name := range current.goCommandTouched {
			names[name] = true
		}
		parent, _ := current.parent.(*overlayEnviron)
		current = parent
	}
	return names
}

func (o *overlayEnviron) goCommandWasTouched(name string) bool {
	for current := o; current != nil; {
		if current.goCommandTouched[name] {
			return true
		}
		current, _ = current.parent.(*overlayEnviron)
	}
	return false
}

// A command-prefix assignment is visible to that command but a restored
// assignment must not count as a lasting shell export for the next Go command.
func (o *overlayEnviron) restoreGoCommandTouch(name string, wasTouched bool) {
	if wasTouched {
		return
	}
	for current := o; current != nil; {
		delete(current.goCommandTouched, name)
		current, _ = current.parent.(*overlayEnviron)
	}
}

func (o *overlayEnviron) isTemp(name string) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		return cur.Temp
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.isTemp(name)
		}
	}
	return false
}

func (o *overlayEnviron) markTemp(name string, prev expand.Variable) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		cur.Temp = true
		cur.Prev = prev
		o.values[normalized] = cur
		return true
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.markTemp(name, prev)
		}
	}
	return false
}

func (o *overlayEnviron) clearTemp(name string) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		if !cur.Temp {
			return false
		}
		cur.Temp = false
		cur.Prev = expand.Variable{}
		o.values[normalized] = cur
		return true
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.clearTemp(name)
		}
	}
	return false
}

func (o *overlayEnviron) setTempVar(name string, vr expand.Variable) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		if !cur.Temp {
			return false
		}
		cur.Variable = vr
		o.values[normalized] = cur
		return true
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.setTempVar(name, vr)
		}
	}
	return false
}

func (o *overlayEnviron) unsetTemp(name string, restoreLocal bool) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		if !cur.Temp {
			if restoreLocal && cur.Variable.Local && cur.Variable.Exported {
				if p, ok := o.parent.(*overlayEnviron); ok && p.isTemp(name) {
					o.values[normalized] = namedVariable{Name: name, Variable: expand.Variable{Local: true, Exported: true}}
					return true
				}
			}
			return false
		}
		switch {
		case restoreLocal && cur.Prev.Declared():
			o.values[normalized] = namedVariable{Name: name, Variable: cur.Prev}
		case restoreLocal && cur.Variable.Local && cur.Variable.Exported:
			o.values[normalized] = namedVariable{Name: name, Variable: expand.Variable{Exported: true}}
		case !restoreLocal && cur.Variable.Local:
			o.values[normalized] = namedVariable{Name: name, Variable: expand.Variable{Local: true}}
		default:
			if o.parent != nil && o.parent.Get(name).Declared() {
				o.values[normalized] = namedVariable{Name: name}
			} else {
				delete(o.values, normalized)
			}
		}
		return true
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.unsetTemp(name, restoreLocal)
		}
	}
	return false
}

func (o *overlayEnviron) restoreTemp(name string, prev expand.Variable) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		if cur.Temp {
			if prev.Declared() {
				o.values[normalized] = namedVariable{Name: name, Variable: prev}
			} else if o.parent != nil && o.parent.Get(name).Declared() {
				// Keep an explicit unset entry when deleting this overlay's
				// temporary value would uncover an inherited variable. The
				// command-prefix assignment must restore the unset state it
				// observed, not the immutable parent environment beneath it.
				o.values[normalized] = namedVariable{Name: name}
			} else {
				delete(o.values, normalized)
			}
			return true
		}
		if !o.funcScope {
			return false
		}
	}
	if o.funcScope {
		if p, ok := o.parent.(*overlayEnviron); ok {
			return p.restoreTemp(name, prev)
		}
	}
	return false
}

// holdsLocally reports whether this overlay itself has an entry for
// name, regardless of any parent environment.
func (o *overlayEnviron) holdsLocally(name string) bool {
	_, ok := o.values[o.normalize(name)]
	return ok
}

func (o *overlayEnviron) hasLocalVar(name string) bool {
	vr, ok := o.values[o.normalize(name)]
	return ok && vr.Local
}

func (o *overlayEnviron) currentLocalVar(name string) (expand.Variable, bool) {
	vr, ok := o.values[o.normalize(name)]
	if !ok || !vr.Local {
		return expand.Variable{}, false
	}
	return vr.Variable, true
}

func (o *overlayEnviron) setNearestLocal(name string, vr expand.Variable) (bool, error) {
	normalized := o.normalize(name)
	if prev, ok := o.values[normalized]; ok && prev.Local {
		vr.Local = true
		return true, o.Set(name, vr)
	}
	if !o.funcScope {
		return false, nil
	}
	p, ok := o.parent.(*overlayEnviron)
	if !ok {
		return false, nil
	}
	return p.setNearestLocal(name, vr)
}

func (o *overlayEnviron) setAttrsBypassReadonly(name string, update func(*expand.Variable)) bool {
	normalized := o.normalize(name)
	if cur, ok := o.values[normalized]; ok {
		update(&cur.Variable)
		o.values[normalized] = cur
		return true
	}
	if p, ok := o.parent.(*overlayEnviron); ok {
		return p.setAttrsBypassReadonly(name, update)
	}
	return false
}

// unsetLocalFromChild implements bash's default `unset` semantics for
// a variable that is local to a *previous* function scope: the local
// is removed entirely, uncovering the variable beneath (an outer local
// or the global). Returns false when no such local exists — e.g. the
// nearest binding is a global, or a readonly local that must keep
// reporting an error through the ordinary path. The walk stops at
// non-function boundaries (subshells) so it never mutates an
// environment shared across goroutines.
func (o *overlayEnviron) unsetLocalFromChild(name string) bool {
	normalized := o.normalize(name)
	if prev, ok := o.values[normalized]; ok {
		if prev.Local && !prev.ReadOnly {
			delete(o.values, normalized)
			return true
		}
		return false
	}
	if !o.funcScope {
		return false
	}
	p, ok := o.parent.(*overlayEnviron)
	return ok && p.unsetLocalFromChild(name)
}

func (o *overlayEnviron) Each(f func(name string, vr expand.Variable) bool) {
	if o.parent != nil {
		o.parent.Each(f)
	}
	for _, vr := range o.values {
		if !f(vr.Name, vr.Variable) {
			return
		}
	}
}

func (o *overlayEnviron) collectNames(seen map[string]bool) map[string]bool {
	if seen == nil {
		seen = map[string]bool{}
	}
	for n := range o.values {
		seen[n] = true
	}
	if p, ok := o.parent.(*overlayEnviron); ok {
		p.collectNames(seen)
	}
	return seen
}

// execEnvValue renders vr as the bytes a child process should receive for it.
//
// This is the OS boundary. A shell variable is a string everywhere else, so for
// every kind but [expand.Object] the value is already what the child needs. An
// object holds a live Go value, which a separate process cannot be handed, so it
// is serialized to JSON on the way out — the child sees a plain string, and one
// it can parse, rather than the empty Str field the value would otherwise
// collapse to.
func execEnvValue(vr expand.Variable) string {
	if vr.Kind == expand.Object {
		return vr.String() // JSON; see [expand.ObjectString]
	}
	return vr.Str
}

func execEnv(env expand.Environ) []string {
	list := make([]string, 0, 64)
	if o, ok := env.(*overlayEnviron); ok && o.parent == nil {
		for _, named := range o.values {
			name, vr := named.Name, named.Variable
			if name == BashyInheritedFdsEnv || name == BashyInheritedHandlesEnv || name == BashyHardIgnoreEnv || name == bashyParentPIDEnv || name == bashyCasedEnv {
				continue
			}
			if !vr.IsSet() && vr.Local && named.Prev.Exported && named.Prev.Kind == expand.String {
				list = append(list, name+"="+named.Prev.String())
			}
			if vr.Exported && (vr.Kind == expand.String || vr.Kind == expand.NameRef || vr.Kind == expand.Object) {
				list = append(list, name+"="+execEnvValue(vr))
			}
		}
		return list
	}
	for name, vr := range env.Each {
		if name == BashyInheritedFdsEnv || name == BashyInheritedHandlesEnv || name == BashyHardIgnoreEnv || name == bashyParentPIDEnv || name == bashyCasedEnv {
			continue
		}
		if !vr.IsSet() && !vr.Local {
			// If a variable is set globally but unset in the
			// runner, we need to ensure it's not part of the final
			// list. Seems like zeroing the element is enough.
			// This is a linear search, but this scenario should be
			// rare, and the number of variables shouldn't be large.
			// Unset *locals* are skipped: like bash's invisible
			// variables they merely shadow within the shell, so an
			// exported global beneath them still reaches children.
			for i, kv := range list {
				if strings.HasPrefix(kv, name+"=") {
					list[i] = ""
				}
			}
		}
		if vr.Exported && (vr.Kind == expand.String || vr.Kind == expand.NameRef || vr.Kind == expand.Object) {
			list = append(list, name+"="+execEnvValue(vr))
		}
	}
	return list
}

// execEnvWithFuncs is like execEnv but also serialises any exported
// shell functions as bash's `BASH_FUNC_<name>%%=() { body; }` entries.
// Used when spawning a child process so a `${THIS_SH} -c '<name>'`
// invocation can re-import the function on startup.
func (r *Runner) execEnvWithFuncs() []string {
	list := execEnv(r.bashPPEnv())
	for _, name := range []string{"BASHOPTS", "SHELLOPTS"} {
		if !r.writeEnv.Get(name).Exported {
			continue
		}
		prefix := name + "="
		for i, kv := range list {
			if strings.HasPrefix(kv, prefix) {
				list[i] = ""
			}
		}
		list = append(list, prefix+r.lookupVar(name).String())
	}
	if len(r.exportedFuncs) == 0 {
		return list
	}
	for name := range r.exportedFuncs {
		body, ok := r.Funcs[name]
		if !ok {
			continue
		}
		list = append(list, "BASH_FUNC_"+name+"%%="+bashExportedFuncValue(name, body))
	}
	return list
}

func bashExportedFuncValue(name string, body *syntax.Stmt) string {
	block, ok := body.Cmd.(*syntax.Block)
	if !ok {
		var b strings.Builder
		syntax.NewPrinter(syntax.SpaceRedirects(true), syntax.BashCompatArith(true)).Print(&b, &syntax.FuncDecl{
			Name: &syntax.Lit{Value: name},
			Body: body,
		})
		s := strings.TrimSpace(b.String())
		if rest, ok := strings.CutPrefix(s, name); ok {
			s = strings.TrimSpace(rest)
		}
		return s
	}
	var b strings.Builder
	b.WriteString("() { \n")
	printer := syntax.NewPrinter(syntax.Indent(4), syntax.SpaceRedirects(true), syntax.BashCompatArith(true))
	for i, st := range block.Stmts {
		var stmt bytes.Buffer
		printer.Print(&stmt, st)
		rendered := bashDeclareFmt(strings.TrimRight(stmt.String(), "\n"), i == len(block.Stmts)-1)
		b.WriteString(rendered)
		b.WriteByte('\n')
		if endsWithHeredocTerminator(stmt.String()) {
			b.WriteByte('\n')
		}
	}
	b.WriteString("}")
	for _, rd := range body.Redirs {
		text := formatRedirect(rd)
		if strings.HasPrefix(text, ">&") {
			text = "1" + text
		} else if strings.HasPrefix(text, "<&") {
			text = "0" + text
		}
		b.WriteByte(' ')
		b.WriteString(text)
	}
	return b.String()
}

func (r *Runner) lookupVar(name string) expand.Variable {
	vr := r.lookupVarUnhosted(name)
	if decl, ok := r.hostedDeclaration(name); ok && decl.Constant {
		vr.ReadOnly = true
	}
	return vr
}

func (r *Runner) lookupVarUnhosted(name string) expand.Variable {
	if name == "" {
		// A nameref whose target is the empty string (or other
		// invalid identifier) can reach here via Variable.Resolve.
		// Bash treats this as "unset"; return the zero value
		// instead of crashing.
		return expand.Variable{}
	}
	// A typed binding is the authority for its name while it is in scope; see
	// sh/interp/bashpp_scope.go. This is the read path expansion takes, so
	// putting the check here is what keeps `$x` and expandEnv.Get from being
	// able to disagree with each other.
	if r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(name); cell != nil {
			return cell.viewVar()
		}
	}
	var vr expand.Variable
	switch name {
	case "#":
		vr.Kind, vr.Str = expand.String, strconv.Itoa(len(r.Params))
	case "@", "*":
		vr.Kind = expand.Indexed
		if r.Params == nil {
			// r.Params may be nil but positional parameters always exist
			vr.List = []string{}
		} else {
			vr.List = r.Params
		}
		vr.Set = len(vr.List) > 0
	case "!":
		// Prefer the real OS PID of the last backgrounded statement's
		// spawned process so the `PID=$!; kill $PID` idiom works
		// against the kernel. Wait briefly for the bg goroutine to
		// either Start an exec.Cmd (typical) or finish without one
		// (`(true) &`); pidReady is closed in either case so this
		// can't hang. Falls back to the legacy "g<N>" sentinel when
		// no real exec ever happened — `wait g<N>` still works against
		// that for pure-builtin backgrounds.
		if bg := r.lastBangProc(); bg != nil {
			select {
			case <-bg.pidReady:
			case <-time.After(250 * time.Millisecond):
			}
			vr.Set = true
			if pid := bg.pid.Load(); pid > 0 {
				vr.Kind, vr.Str = expand.String, strconv.FormatInt(pid, 10)
			} else {
				vr.Kind, vr.Str = expand.String, "g"+strconv.Itoa(bg.jobID)
			}
		}
	case "?":
		vr.Kind, vr.Str = expand.String, strconv.Itoa(int(r.lastExit.code))
	case "$":
		if r.deterministic {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(int(r.deterministicSeed&0x7fff))
		} else {
			pid := int64(os.Getpid())
			if r.asyncList && r.execReplacement != nil {
				if replacement := r.execReplacement.current.Load(); replacement != nil {
					<-replacement.ready
					if replacementPid := replacement.pid.Load(); replacementPid > 0 {
						pid = replacementPid
						if r.asyncProc != nil {
							replacement.observer.Store(r.asyncProc)
						}
					}
				}
			}
			vr.Kind, vr.Str = expand.String, strconv.FormatInt(pid, 10)
		}
	case "PPID":
		vr.Kind, vr.Str = expand.String, strconv.Itoa(r.parentPID)
	case "-":
		// Bash's $- expands to the single-letter forms of all
		// currently-set option flags, in bash's canonical order.
		// Always-on defaults (`h` — hashall, `B` — braceexpand)
		// are surfaced too so the output is non-empty even on a
		// fresh shell.
		var sb strings.Builder
		if r.opts[optAllExport] {
			sb.WriteByte('a')
		}
		noOpEnabled := func(name string) bool {
			if v, ok := r.noOpSetState[name]; ok {
				return v
			}
			return noOpSetOptions[name]
		}
		if noOpEnabled("notify") {
			sb.WriteByte('b')
		}
		if r.opts[optErrExit] {
			sb.WriteByte('e')
		}
		if r.opts[optNoGlob] {
			sb.WriteByte('f')
		}
		if noOpEnabled("hashall") {
			sb.WriteByte('h')
		}
		if r.interactiveShell {
			sb.WriteByte('i')
		}
		if r.opts[optKeyword] {
			sb.WriteByte('k')
		}
		if r.noOpSetState["monitor"] && r.interactiveShell {
			sb.WriteByte('m')
		}
		if r.opts[optNoExec] {
			sb.WriteByte('n')
		}
		if noOpEnabled("privileged") {
			sb.WriteByte('p')
		}
		if noOpEnabled("onecmd") {
			sb.WriteByte('t')
		}
		if r.opts[optNoUnset] {
			sb.WriteByte('u')
		}
		if noOpEnabled("verbose") {
			sb.WriteByte('v')
		}
		if r.opts[optXTrace] {
			sb.WriteByte('x')
		}
		if r.opts[optPipeFail] {
			// pipefail has no single-letter form; bash 5.3
			// emits nothing for it in $-.
		}
		if noOpEnabled("braceexpand") {
			sb.WriteByte('B')
		}
		if r.opts[optNoClobber] {
			sb.WriteByte('C')
		}
		if noOpEnabled("errtrace") {
			sb.WriteByte('E')
		}
		if noOpEnabled("histexpand") && r.interactiveShell {
			sb.WriteByte('H')
		}
		if noOpEnabled("physical") {
			// `set -P` / `set -o physical` is accept-and-ignore, but
			// bash surfaces it in $- once toggled on.
			sb.WriteByte('P')
		}
		if noOpEnabled("functrace") {
			sb.WriteByte('T')
		}
		if r.commandString {
			sb.WriteByte('c')
		} else if r.standardInput {
			sb.WriteByte('s')
		}
		vr.Kind, vr.Str = expand.String, sb.String()
	case "RANDOM": // not for cryptographic use
		if r.randomSeeded {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(r.bashRandom())
		} else if r.deterministic && r.deterministicRng != nil {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(int(r.deterministicRng.Uint64()&0x7fff))
		} else {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(mathrand.IntN(32767))
		}
	case "SRANDOM": // pseudo-random generator from the system
		if r.deterministic && r.deterministicRng != nil {
			vr.Kind, vr.Str = expand.String, strconv.FormatUint(r.deterministicRng.Uint64()&0xffffffff, 10)
		} else {
			var p [4]byte
			cryptorand.Read(p[:])
			n := binary.NativeEndian.Uint32(p[:])
			vr.Kind, vr.Str = expand.String, strconv.FormatUint(uint64(n), 10)
		}
	case "BASHPID":
		if r.deterministic {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(int(r.deterministicSeed&0x7fff))
		} else {
			// Real bash returns the OS PID, which differs in a forked
			// subshell. Our subshells are goroutines (same OS PID), so
			// shift by subshellLevel to make BASHPID differ per
			// subshell layer — scripts that compare $BASHPID across
			// boundaries (the canonical use case) keep working.
			vr.Kind, vr.Str = expand.String, strconv.Itoa(os.Getpid()+r.subshellLevel)
		}
	case "SECONDS":
		if r.deterministic {
			vr.Kind, vr.Str = expand.String, "0"
		} else {
			vr.Kind, vr.Str = expand.String, strconv.FormatInt(int64(time.Since(r.startTime).Seconds()), 10)
		}
	case "EPOCHSECONDS":
		if r.deterministic {
			vr.Kind, vr.Str = expand.String, strconv.FormatInt(r.startTime.Unix(), 10)
		} else {
			vr.Kind, vr.Str = expand.String, strconv.FormatInt(time.Now().Unix(), 10)
		}
	case "EPOCHREALTIME":
		if r.deterministic {
			vr.Kind, vr.Str = expand.String, fmt.Sprintf("%d.000000", r.startTime.Unix())
		} else {
			now := time.Now()
			vr.Kind, vr.Str = expand.String, fmt.Sprintf("%d.%06d", now.Unix(), now.Nanosecond()/1000)
		}
	case "BASH_MONOSECONDS":
		// Bash 5.3 monotonic clock — seconds since an unspecified point,
		// unaffected by wall-clock adjustments. Reusing the shell's
		// monotonic start time keeps the value stable across calls and
		// deterministic mode.
		vr.Kind, vr.Str = expand.String, strconv.FormatInt(int64(time.Since(r.startTime).Seconds()), 10)
	case "BASH_SUBSHELL":
		vr.Kind, vr.Str = expand.String, strconv.Itoa(r.subshellLevel)
	case "BASH_ARGV0":
		vr.Kind, vr.Str = expand.String, r.shellArgv0()
	case "GROUPS":
		vr.Kind = expand.Indexed
		vr.ReadOnly = true
		vr.List = userGroups()
	case "HOSTNAME":
		h, _ := os.Hostname()
		vr.Kind, vr.Str = expand.String, h
	case "COLUMNS":
		// Bash exposes the terminal width via $COLUMNS. We query the
		// controlling stdin/stdout/stderr for a TTY size; if none of
		// them is a terminal, the variable stays empty so scripts can
		// detect "no TTY" via [[ -z $COLUMNS ]].
		if w := terminalWidth(); w > 0 {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(w)
		}
	case "LINES":
		if _, h := terminalSize(); h > 0 {
			vr.Kind, vr.Str = expand.String, strconv.Itoa(h)
		}
	case "LINENO":
		vr.Kind = expand.String
		vr.ReadOnly = true
		if r.curStmtPos.IsValid() {
			vr.Str = strconv.FormatUint(uint64(r.curStmtPos.Line()), 10)
		} else {
			vr.Str = "0"
		}
	case "HOSTTYPE":
		vr.Kind, vr.Str = expand.String, runtime.GOARCH
	case "MACHTYPE":
		vr.Kind, vr.Str = expand.String, runtime.GOARCH+"-unknown-"+runtime.GOOS
	case "OSTYPE":
		vr.Kind, vr.Str = expand.String, runtime.GOOS
	case "SHELLOPTS":
		var opts []string
		// Append the long names of POSIX-table options that are
		// currently enabled.
		for i, opt := range &posixOptsTable {
			if r.opts[i] && opt.name != "" {
				opts = append(opts, opt.name)
			}
		}
		// Bash 5.3 also includes a curated subset of the
		// default-on no-op options — braceexpand, hashall,
		// interactive-comments — so scripts can read
		// SHELLOPTS without having to know which options are
		// "real" vs. compat-only.
		for _, name := range []string{"braceexpand", "hashall", "interactive-comments"} {
			if enabled, ok := r.noOpSetState[name]; ok && !enabled {
				continue
			}
			opts = append(opts, name)
		}
		// Accept-and-ignore options that bash nonetheless reflects in
		// SHELLOPTS once toggled on (`shopt -so physical`).
		if r.noOpSetState["physical"] {
			opts = append(opts, "physical")
		}
		slices.Sort(opts)
		vr.Kind, vr.Str = expand.String, strings.Join(opts, ":")
		vr.ReadOnly = true
	case "BASHOPTS":
		var opts []string
		for i, opt := range bashOptsTable {
			if r.opts[len(posixOptsTable)+i] {
				opts = append(opts, opt.name)
			}
		}
		vr.Kind, vr.Str = expand.String, strings.Join(opts, ":")
		vr.ReadOnly = true
	case "BASH_VERSINFO":
		vr.Kind = expand.Indexed
		vr.ReadOnly = true
		vr.List = []string{"5", "3", "0", "1", "release", "bashy"}
	case "FUNCNAME":
		vr.Kind = expand.Indexed
		// Bash appends "main" as the outermost frame in FUNCNAME so
		// that scripts can reliably introspect the call chain back
		// to the top-level. Match that — but only when there's at
		// least one function on the stack; an empty FUNCNAME stays
		// unset, like bash.
		if len(r.callStack) > 0 {
			names := make([]string, len(r.callStack)+1)
			for i, f := range r.callStack {
				names[len(r.callStack)-1-i] = f.funcName
			}
			names[len(r.callStack)] = "main"
			vr.List = names
		}
	case "BASH_SOURCE":
		vr.Kind = expand.Indexed
		// A Bash 5.3 command string has no top-level source entry. Functions
		// defined by -c do have one entry per function frame, each carrying the
		// command's argv0; unlike a file script there is no trailing main frame.
		if r.commandString {
			if len(r.callStack) > 0 {
				sources := make([]string, len(r.callStack))
				for i, f := range r.callStack {
					sources[len(r.callStack)-1-i] = f.source
				}
				vr.List = sources
			}
			break
		}
		if len(r.callStack) > 0 {
			sources := make([]string, len(r.callStack)+1)
			for i, f := range r.callStack {
				sources[len(r.callStack)-1-i] = f.source
			}
			outer := r.callStack[0].source
			if r.callStack[0].callerSource != "" {
				outer = r.callStack[0].callerSource
			}
			sources[len(r.callStack)] = outer
			vr.List = sources
		} else if r.filename != "" {
			// A file-backed top-level script has one BASH_SOURCE element even
			// before it enters a function.  Command strings and stdin have no
			// filename and therefore keep Bash's empty array.
			vr.List = []string{r.filename}
		}
	case "BASH_LINENO":
		vr.Kind = expand.Indexed
		if len(r.callStack) > 0 {
			lines := make([]string, len(r.callStack)+1)
			for i, f := range r.callStack {
				lines[len(r.callStack)-1-i] = strconv.FormatUint(uint64(f.line), 10)
			}
			lines[len(r.callStack)] = "0"
			vr.List = lines
		}
	case "BASH_ARGC":
		vr.Kind = expand.Indexed
		extdebug, _ := r.bashOptByName("extdebug")
		if extdebug != nil && *extdebug && len(r.callStack) > 0 {
			counts := make([]string, len(r.callStack))
			for i := range r.callStack {
				f := r.callStack[len(r.callStack)-1-i]
				counts[i] = strconv.Itoa(len(f.args))
			}
			vr.List = counts
		}
	case "BASH_ARGV":
		vr.Kind = expand.Indexed
		extdebug, _ := r.bashOptByName("extdebug")
		if extdebug != nil && *extdebug && len(r.callStack) > 0 {
			var argv []string
			for i := len(r.callStack) - 1; i >= 0; i-- {
				args := r.callStack[i].args
				for j := len(args) - 1; j >= 0; j-- {
					argv = append(argv, args[j])
				}
			}
			vr.List = argv
		}
	case "PIPESTATUS":
		vr.Kind = expand.Indexed
		if r.pipeStatus != nil {
			vr.List = r.pipeStatus
		}
	case "DIRSTACK":
		// Bash exposes DIRSTACK with the top-of-stack at index 0,
		// matching `dirs` output. r.dirStack stores the stack with
		// the top at the end, so reverse it.
		vr.Kind = expand.Indexed
		vr.List = make([]string, len(r.dirStack))
		for i, d := range r.dirStack {
			vr.List[len(r.dirStack)-1-i] = d
		}
	case "BASH_ALIASES":
		vr.Kind = expand.Associative
		// Bash's alias table is a hash_create(0) table with
		// DEFAULT_HASH_BUCKETS (128) buckets, so ${BASH_ALIASES[@]} /
		// ${!BASH_ALIASES[@]} iterate in 128-bucket order, not the
		// 1024-bucket order of a fresh `declare -A`.
		vr.AssocBuckets = 128
		vr.Map = make(map[string]string, len(r.alias))
		for k, als := range r.alias {
			vr.Map[k] = aliasValue(als)
		}
	case "BASH_CMDS":
		vr.Kind = expand.Associative
		// Bash's command hash table iterates like a 256-bucket table.
		vr.AssocBuckets = 256
		vr.Map = make(map[string]string, len(r.cmdHashTable))
		for name, entry := range r.cmdHashTable {
			vr.Map[name] = entry.path
		}
	case "0":
		vr.Kind, vr.Str = expand.String, r.shellArgv0()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if i := int(name[0] - '1'); i < len(r.Params) {
			vr.Kind = expand.String
			vr.Str = r.Params[i]
		}
	default:
		// Handle multi-digit positional parameters: ${10}, ${11}, etc.
		if len(name) > 1 && name[0] >= '1' && name[0] <= '9' {
			allDigits := true
			for _, c := range name {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				n, _ := strconv.Atoi(name)
				if n > 0 && n <= len(r.Params) {
					vr.Kind = expand.String
					vr.Str = r.Params[n-1]
				}
			}
		}
	}
	if vr.Kind != expand.Unknown {
		if name == "@" || name == "*" {
			return vr
		}
		vr.Set = true
		return vr
	}
	if vr := r.writeEnv.Get(name); vr.Declared() {
		return vr
	}
	return expand.Variable{}
}

func (r *Runner) envGet(name string) string {
	return r.lookupVar(name).String()
}

func (r *Runner) bashRandom() int {
	seed := r.randomSeed
	if seed == 0 {
		seed = 123459876
	}
	h := seed / 127773
	l := seed % 127773
	t := int32(16807*l) - int32(2836*h)
	if t < 0 {
		seed = uint32(t + 0x7fffffff)
	} else {
		seed = uint32(t)
	}
	r.randomSeed = seed
	return int(((seed >> 16) ^ (seed & 0xffff)) & 0x7fff)
}

// printLocalVars writes the local variables of the current function
// scope to stdout in bash's `local` listing format:
//
//	name=value                                  (scalar)
//	name=([0]="x" [1]="y")                      (indexed array)
//	name=([k]="v")                              (associative array)
//
// Iteration order is sorted by name to match bash. Only the current
// overlay is enumerated; vars inherited from the parent scope are
// skipped — those aren't local.
func (r *Runner) printLocalVars() {
	overlay, ok := r.writeEnv.(*overlayEnviron)
	if !ok || overlay == nil {
		return
	}
	if r.localOptsActive() {
		r.outf("local -\n")
	}
	names := make([]string, 0, len(overlay.values))
	for k := range overlay.values {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, name := range names {
		nv := overlay.values[name]
		// Only variables that are genuinely local to the current
		// function scope are listed. A background/pipe subshell
		// flattens the entire parent environment into values (see
		// newOverlayEnviron), so non-local globals appear here with
		// Local=false; bash lists only the locals.
		if !nv.Variable.Local {
			continue
		}
		if !nv.Variable.IsSet() {
			continue
		}
		r.outf("%s\n", formatLocalVar(name, nv.Variable))
	}
}

func (r *Runner) printSetVars() {
	var names []string
	seen := map[string]bool{}
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if !vr.IsSet() && !(vr.Kind == expand.Associative && len(vr.Map) > 0) {
			return true
		}
		if seen[name] {
			return true
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	slices.Sort(names)
	for _, name := range names {
		vr := r.writeEnv.Get(name)
		switch vr.Kind {
		case expand.Indexed:
			r.outf("%s=(", name)
			first := true
			for _, i := range vr.IndexedIndexes() {
				if !first {
					r.out(" ")
				}
				first = false
				r.outf("[%d]=%s", i, bashSetQuote(vr.IndexedElem(i)))
			}
			if !first {
				r.out(" ")
			}
			r.out(")\n")
		case expand.Associative:
			r.outf("%s=(", name)
			first := true
			for _, k := range vr.AssocKeysForDeclare() {
				if !first {
					r.out(" ")
				}
				r.outf("[%s]=%s", bashAssocKeyQuote(k), bashDeclareQuote(vr.Map[k]))
				first = false
			}
			if !first {
				r.out(" ")
			}
			r.out(")\n")
		default:
			// An object lists as the string it coerces to (its JSON), not
			// as the empty Str field it does not use.
			r.outf("%s=%s\n", name, bashSetQuote(scalarValue(vr)))
		}
	}
}

// scalarValue is the string a scalar-shaped variable presents as to the shell:
// its own Str, except for an [expand.Object], which coerces to JSON. Use it
// wherever a variable is rendered back out as shell text, so that an object
// reads as its value rather than as an empty string.
func scalarValue(vr expand.Variable) string {
	if vr.Kind == expand.Object {
		return vr.String()
	}
	return vr.Str
}

// printArrayVars writes every variable of the requested array kind
// (`-A` associative, `-a` indexed) to stdout in declare -p format,
// sorted by name. Built-in bash arrays (BASH_ALIASES, BASH_CMDS,
// BASH_ARGC, …) appear alongside user-declared ones so scripts
// that probe `declare -A` see them, unless readonlyOnly filters them.
func (r *Runner) printArrayVars(kind string, readonlyOnly, readonlyKeyword bool) {
	want := expand.Associative
	if kind == "-a" {
		want = expand.Indexed
	}
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
		}
	}
	// Built-in arrays that bash always exposes — pull them so they
	// list even when the user hasn't touched the variable.
	if want == expand.Associative {
		for _, n := range []string{"BASH_ALIASES", "BASH_CMDS"} {
			add(n)
		}
	} else {
		for _, n := range []string{"BASH_ARGC", "BASH_ARGV", "BASH_LINENO", "BASH_SOURCE", "BASH_VERSINFO", "DIRSTACK", "FUNCNAME"} {
			add(n)
		}
	}
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if readonlyOnly && !vr.ReadOnly {
			return true
		}
		if vr.Kind == want {
			add(name)
		}
		return true
	})
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		vr := r.lookupVar(name)
		if want == expand.Indexed {
			if builtin, ok := bashBuiltinArrayDeclVar(name, r); ok {
				vr = builtin
			}
		}
		if vr.Kind != want {
			continue
		}
		if readonlyOnly && !vr.ReadOnly {
			continue
		}
		isBuiltin := false
		switch name {
		case "BASH_ALIASES", "BASH_CMDS", "BASH_ARGC", "BASH_ARGV", "BASH_LINENO", "BASH_SOURCE", "DIRSTACK", "FUNCNAME":
			isBuiltin = true
		}
		if name == "FUNCNAME" && !vr.Set && len(vr.List) == 0 {
			isBuiltin = false
		}
		if readonlyKeyword {
			r.outf("%s\n", formatReadonlyArrayVar(name, vr))
		} else {
			r.outf("%s\n", formatDeclareVar(name, vr, isBuiltin))
		}
	}
}

func formatReadonlyArrayVar(name string, vr expand.Variable) string {
	line := formatDeclareVar(name, vr, false)
	switch vr.Kind {
	case expand.Indexed:
		return strings.Replace(line, "declare -ar ", "readonly -a ", 1)
	case expand.Associative:
		return strings.Replace(line, "declare -Ar ", "readonly -A ", 1)
	default:
		return line
	}
}

func bashBuiltinArrayDeclVar(name string, r *Runner) (expand.Variable, bool) {
	switch name {
	case "BASH_ARGC", "BASH_ARGV":
		return expand.Variable{Kind: expand.Indexed, Set: true, List: []string{}}, true
	case "BASH_LINENO":
		vr := r.lookupVar(name)
		if len(vr.List) == 0 {
			vr.Set = true
			vr.List = []string{"0"}
		}
		return vr, true
	case "BASH_SOURCE":
		vr := r.lookupVar(name)
		if len(vr.List) == 0 && r.filename != "" {
			vr.Set = true
			vr.List = []string{r.filename}
		}
		return vr, true
	case "DIRSTACK":
		return expand.Variable{Kind: expand.Indexed, Set: true, List: []string{}}, true
	case "FUNCNAME":
		vr := r.lookupVar(name)
		if len(vr.List) == 0 {
			vr.Set = false
			vr.List = nil
		}
		return vr, true
	}
	return expand.Variable{}, false
}

// printNamerefVars writes every nameref-typed variable to stdout in
// bash's `declare -n NAME="target"` format, sorted by name. Used
// by `typeset -n` / `declare -n` with no arguments.
func (r *Runner) printNamerefVars() {
	var names []string
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if vr.Kind == expand.NameRef {
			names = append(names, name)
		}
		return true
	})
	slices.Sort(names)
	for _, name := range names {
		vr := r.lookupVar(name)
		if vr.Str == "" {
			r.outf("declare -n %s\n", name)
		} else {
			r.outf("declare -n %s=%s\n", name, bashDeclareQuote(vr.Str))
		}
	}
}

func (r *Runner) printDeclareVars(modes []string) {
	var names []string
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if vr.Declared() && declareVarMatchesModes(vr, modes) {
			names = append(names, name)
		}
		return true
	})
	slices.Sort(names)
	for _, name := range names {
		vr := r.lookupVar(name)
		if vr.Declared() && declareVarMatchesModes(vr, modes) {
			r.outf("%s\n", formatDeclareVar(name, vr, false))
		}
	}
}

func declareVarMatchesModes(vr expand.Variable, modes []string) bool {
	for _, mode := range modes {
		switch mode {
		case "-n":
			if vr.Kind != expand.NameRef {
				return false
			}
		case "-r":
			if !vr.ReadOnly {
				return false
			}
		case "-x":
			if !vr.Exported {
				return false
			}
		case "-a":
			if vr.Kind != expand.Indexed {
				return false
			}
		case "-A":
			if vr.Kind != expand.Associative {
				return false
			}
		case "-i":
			if !vr.Integer {
				return false
			}
		}
	}
	return true
}

// declareListFilterModes folds a declare-family valType flag that
// selects variables by attribute (-n nameref, -i integer) into the
// mode filter used by the no-name `declare -p` listing. -a/-A are
// handled by printArrayVars and intentionally excluded here.
func declareListFilterModes(modes []string, valType string) []string {
	switch valType {
	case "-n", "-i":
		return append(slices.Clone(modes), valType)
	}
	return modes
}

func (r *Runner) printExportVars() {
	var names []string
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported {
			names = append(names, name)
		}
		return true
	})
	slices.Sort(names)
	for _, name := range names {
		vr := r.lookupVar(name)
		if vr.Exported {
			r.outf("%s\n", formatPosixAttrVar("export", name, vr))
		}
	}
}

func (r *Runner) printReadonlyVars(posix bool) {
	seen := map[string]bool{}
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if vr.ReadOnly {
			seen[name] = true
		}
		return true
	})
	for _, name := range []string{"BASHOPTS", "BASH_VERSINFO", "GROUPS", "SHELLOPTS"} {
		if r.lookupVar(name).ReadOnly {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		vr := r.lookupVar(name)
		if vr.ReadOnly {
			if posix {
				r.outf("%s\n", formatPosixAttrVar("readonly", name, vr))
			} else {
				r.outf("%s\n", formatDeclareVar(name, vr, false))
			}
		}
	}
}

func formatPosixAttrVar(keyword, name string, vr expand.Variable) string {
	if !vr.Set {
		return keyword + " " + name
	}
	return keyword + " " + name + "=" + bashDeclareQuote(vr.Str)
}

// formatLocalVar renders a single variable in bash 5.3's `local`
// listing shape: `declare <flags> name=value`. `<flags>` covers
// `-a` (indexed), `-A` (associative), `-i`/`-r`/`-x`/etc., or `--`
// when no other flag is set. Values use the same quoting rules as
// `declare -p`.
func formatLocalVar(name string, vr expand.Variable) string {
	return formatDeclareVar(name, vr, false)
}

func formatDeclareVar(name string, vr expand.Variable, forceEmptyArrayValue bool) string {
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
	case expand.Indexed:
		if !vr.Set && !forceEmptyArrayValue && len(vr.List) == 0 {
			return b.String()
		}
		b.WriteByte('=')
		b.WriteByte('(')
		first := true
		for _, i := range vr.IndexedIndexes() {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			fmt.Fprintf(&b, "[%d]=%s", i, bashDeclareQuote(vr.IndexedElem(i)))
		}
		b.WriteByte(')')
	case expand.Associative:
		if !vr.Set && !forceEmptyArrayValue && len(vr.Map) == 0 {
			return b.String()
		}
		b.WriteByte('=')
		b.WriteByte('(')
		first := true
		for _, k := range vr.AssocKeysForDeclare() {
			if !first {
				b.WriteByte(' ')
			}
			first = false
			fmt.Fprintf(&b, "[%s]=%s", bashAssocKeyQuote(k), bashDeclareQuote(vr.Map[k]))
		}
		if !first {
			b.WriteByte(' ')
		}
		b.WriteByte(')')
	default:
		if !vr.Set {
			return b.String()
		}
		if vr.Kind == expand.NameRef && vr.Str == "" {
			return b.String()
		}
		b.WriteByte('=')
		b.WriteString(bashDeclareQuote(scalarValue(vr)))
	}
	return b.String()
}

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

func variableJSON(name string, vr expand.Variable) map[string]any {
	flags := vr.Flags()
	if flags == "" {
		flags = "-"
	}
	obj := map[string]any{
		"name":        name,
		"flags":       flags,
		"set":         vr.Set,
		"exported":    vr.Exported,
		"readonly":    vr.ReadOnly,
		"integer":     vr.Integer,
		"local":       vr.Local,
		"uppercase":   vr.Upper,
		"lowercase":   vr.Lower,
		"capitalized": vr.Capitalize,
	}
	switch vr.Kind {
	case expand.Indexed:
		obj["kind"] = "indexed"
		values := make(map[string]string, vr.IndexedCount())
		for _, i := range vr.IndexedIndexes() {
			values[strconv.Itoa(i)] = vr.IndexedElem(i)
		}
		obj["value"] = values
	case expand.Associative:
		obj["kind"] = "associative"
		m := make(map[string]string, len(vr.Map))
		for k, v := range vr.Map {
			m[k] = v
		}
		obj["value"] = m
	case expand.NameRef:
		obj["kind"] = "nameref"
		obj["value"] = vr.Str
	case expand.String:
		obj["kind"] = "string"
		obj["value"] = vr.Str
	default:
		obj["kind"] = "unknown"
	}
	return obj
}

func (r *Runner) variablesJSON(setOnly bool) []map[string]any {
	var names []string
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		if setOnly && !vr.IsSet() {
			return true
		}
		if !setOnly && !vr.Declared() {
			return true
		}
		names = append(names, name)
		return true
	})
	slices.Sort(names)
	var vars []map[string]any
	for _, name := range names {
		vars = append(vars, variableJSON(name, r.writeEnv.Get(name)))
	}
	return vars
}

func (r *Runner) functionJSON(name string) map[string]any {
	obj := map[string]any{
		"name":     name,
		"readonly": r.readonlyFuncs[name],
		"exported": r.exportedFuncs[name],
	}
	if body := r.Funcs[name]; body != nil {
		var b strings.Builder
		syntax.NewPrinter().Print(&b, body)
		obj["body"] = strings.TrimRight(b.String(), "\n")
		obj["line"] = int(body.Pos().Line())
	}
	return obj
}

func (r *Runner) functionsJSON(readonlyOnly, exportedOnly bool) []map[string]any {
	names := make([]string, 0, len(r.Funcs))
	for name := range r.Funcs {
		if readonlyOnly && !r.readonlyFuncs[name] {
			continue
		}
		if exportedOnly && !r.exportedFuncs[name] {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	funcs := make([]map[string]any, 0, len(names))
	for _, name := range names {
		funcs = append(funcs, r.functionJSON(name))
	}
	return funcs
}

func (r *Runner) globalWriteEnv() expand.WriteEnviron {
	env := r.writeEnv
	for {
		ol, ok := env.(*overlayEnviron)
		if !ok || ol.parent == nil {
			break
		}
		nextWE, ok := ol.parent.(*overlayEnviron)
		if !ok {
			break
		}
		env = nextWE
	}
	wenv, _ := env.(expand.WriteEnviron)
	return wenv
}

func (r *Runner) lookupGlobalVar(name string) expand.Variable {
	wenv := r.globalWriteEnv()
	if wenv == nil {
		return r.lookupVar(name)
	}
	if vr := wenv.Get(name); vr.Declared() {
		return vr
	}
	return expand.Variable{}
}

// setGlobalVarString assigns name=value at the outermost (global)
// scope, bypassing any in-flight function overlays. Used for bash
// 5.3 `{var}` redirections, which set the captured fd globally even
// when the redirect is inside a function body.
func (r *Runner) setGlobalVarString(name, value string) {
	if n, _ := r.lookupVar(name).Resolve(r.writeEnv); n != "" {
		name = n
	}
	vr := expand.Variable{Set: true, Kind: expand.String, Str: value}
	r.setGlobalVar(name, vr)
}

// bashShoptEnabled reports the state of a bash `shopt` option by name.
func (r *Runner) bashShoptEnabled(name string) bool {
	for i, opt := range bashOptsTable {
		if opt.name == name {
			return r.opts[len(posixOptsTable)+i]
		}
	}
	return false
}

func (r *Runner) delVar(name string) {
	if decl, ok := r.hostedDeclaration(name); ok {
		r.refuseDeclarationUnset(name, decl.Constant)
		return
	}
	// A declaration cannot be undeclared. Go has no `unset`, and letting one
	// through would leave the name bound-but-unset in a block that still owns
	// it, so a later `var x = 1` in the same block would fail as a
	// redeclaration of something the script believes it deleted. A `const` is
	// refused one layer earlier still, by the unset builtin's own readonly
	// check — which is what `const` marking the variable readonly buys.
	if r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(name); cell != nil {
			r.refuseDeclarationUnset(name, cell.viewConstant())
			return
		}
	}
	if o, ok := r.writeEnv.(*overlayEnviron); ok && r.tempEnv[name] {
		restoreLocal := !r.opts[optPosix]
		if o.unsetTemp(name, restoreLocal) {
			return
		}
	}
	// Unsetting a variable that is local to a *previous* function scope
	// removes that local entirely, uncovering the variable beneath
	// (bash's default dynamic-unset behavior). With shopt
	// localvar_unset, the local is instead marked unset and keeps
	// shadowing, which is what the ordinary path below implements.
	if !r.bashShoptEnabled("localvar_unset") {
		if o, ok := r.writeEnv.(*overlayEnviron); ok && o.funcScope && !o.holdsLocally(name) {
			if p, ok := o.parent.(*overlayEnviron); ok && p.unsetLocalFromChild(name) {
				return
			}
		}
	}
	if err := r.writeEnv.Set(name, expand.Variable{}); err != nil {
		r.errf("%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), name, err)
		r.exit.code = 1
		return
	}
}

// forceUnset removes name from this overlay (or the nearest parent
// overlay that holds it), ignoring the readonly attribute and without
// following namerefs. Used by coproc reaping, which unbinds
// `<NAME>_PID` the way bash's unbind_variable_noref does.
func (o *overlayEnviron) forceUnset(name string) bool {
	normalized := o.normalize(name)
	if _, ok := o.values[normalized]; ok {
		delete(o.values, normalized)
		return true
	}
	if p, ok := o.parent.(*overlayEnviron); ok {
		return p.forceUnset(name)
	}
	return false
}

// forceDelVar removes a variable bypassing the readonly check, used when
// bash's internal unbind paths (rather than the `unset` builtin) drop a
// variable. A no-op when the write environment isn't an overlayEnviron.
func (r *Runner) forceDelVar(name string) {
	if o, ok := r.writeEnv.(*overlayEnviron); ok {
		o.forceUnset(name)
	}
}

// splitArrayRef recognises `name[index]` references used by builtins
// like `unset` to address a single array element. Returns the bare
// variable name and the (unparsed) index expression; ok is false if
// the input is not in the `NAME[INDEX]` form.
// assocSubscriptBracketIssue scans key — the already quote-removed
// subscript of a declare-family builtin target (`declare name[key]=value`)
// — the way bash's skipsubscript does, and reports two kinds of trouble.
//
// closesEarly is true when a `]` drops the bracket depth to zero before the
// end of the key (`foo]bar`, `]`), so the real closing bracket is followed
// by junk. unbalancedOpen is true when a `[` is left open at the end
// (`foo[bar`). Balanced nested brackets (`a[0]`) report neither.
//
// Builtin arguments are word-expanded before the builtin parses them, so
// the quotes that protect brackets in a direct assignment are gone. bash
// always rejects an early close, but only rejects a dangling open bracket
// when assoc_expand_once is unset (with it set, the once-expanded subscript
// is taken literally and the stray `[` survives).
func assocSubscriptBracketIssue(key string) (closesEarly, unbalancedOpen bool) {
	depth := 1
	for i := 0; i < len(key); i++ {
		switch key[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return true, false
			}
		}
	}
	return false, depth != 1
}

func splitArrayRef(s string) (name, idx string, ok bool) {
	lb := strings.IndexByte(s, '[')
	if lb <= 0 || !strings.HasSuffix(s, "]") {
		return "", "", false
	}
	return s[:lb], s[lb+1 : len(s)-1], true
}

func indexedNegativeOffset(vr expand.Variable, n int) int {
	indexes := vr.IndexedIndexes()
	if len(indexes) == 0 {
		return n
	}
	return indexes[len(indexes)-1] + 1 + n
}

// unsetArrayElem removes a single element from an indexed or
// associative array. For indexed arrays the index is arithmetic;
// for associative arrays it's a literal key. `name[*]` / `name[@]`
// unset the entire variable (matching bash).
func (r *Runner) unsetArrayElem(name, idx string) bool {
	if idx == "*" || idx == "@" {
		vr := r.lookupVar(name)
		// Bash 5.2 empties the array; BASH_COMPAT=51 and earlier unset the
		// whole variable (quotearray3.sub).
		if vr.Kind == expand.Indexed && !r.bashCompatAtMost(51) {
			vr.Set = true
			vr.List = []string{}
			vr.ListSet = nil
			r.setVar(name, vr)
		} else {
			r.delVar(name)
		}
		return true
	}
	vr := r.lookupVar(name)
	switch vr.Kind {
	case expand.Indexed:
		// This subscript is already expanded; a residual $( … )/`…`
		// command substitution is literal text bash treats as a failed
		// arithmetic operand and never re-executes. Report it directly
		// and leave the element untouched.
		if tok := expand.ArithmCmdSubstToken(idx); tok != "" {
			r.expandErr(&expand.ArithmError{
				Text: idx,
				Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tok),
			})
			return false
		}
		n64, err := r.arithFromString(idx)
		if err != nil {
			r.errf("%sunset: [%s]: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), idx)
			return false
		}
		n := int(n64)
		if n < 0 {
			n = indexedNegativeOffset(vr, n)
			if n < 0 {
				r.errf("%sunset: [%s]: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), idx)
				return false
			}
		}
		if !vr.IndexedSet(n) {
			return true
		}
		set := vr.DenseListSet()
		delete(set, n)
		if n < len(vr.List) {
			vr.List[n] = ""
			for len(vr.List) > 0 && !set[len(vr.List)-1] {
				vr.List = vr.List[:len(vr.List)-1]
			}
		} else if vr.ListMap != nil {
			vr.ListMap = vr.CloneListMap()
			delete(vr.ListMap, n)
		}
		vr.ListSet = set
		if len(vr.List) == 0 && len(vr.ListMap) == 0 {
			vr.ListSet = nil
		}
	case expand.Associative:
		if _, ok := vr.Map[idx]; ok {
			delete(vr.Map, idx)
		}
	default:
		return true
	}
	r.setVar(name, vr)
	return true
}

func (r *Runner) setVarString(name, value string) {
	if base, idx, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
		if r.arrayExpandOnceIndexedSubscriptErr(idx) {
			return
		}
		r.setVarWithIndex(r.lookupVar(base), base, r.arrayTargetIndex(idx), expand.Variable{Set: true, Kind: expand.String, Str: value}, false)
		return
	}
	r.setVar(name, expand.Variable{Set: true, Kind: expand.String, Str: value})
}

func (r *Runner) arrayTargetIndex(idx string) syntax.ArithmExpr {
	return parseArrayTargetIndex(idx)
}

func parseArrayTargetIndex(idx string) syntax.ArithmExpr {
	if !strings.ContainsAny(idx, "$'\"`\\") {
		return &syntax.Word{Parts: []syntax.WordPart{
			&syntax.Lit{Value: idx},
		}}
	}
	src := "x[" + idx + "]=_"
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	if file, err := parser.Parse(strings.NewReader(src), ""); err == nil && len(file.Stmts) == 1 {
		if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 1 && call.Assigns[0].Index != nil {
			return call.Assigns[0].Index
		}
	}
	return &syntax.Word{Parts: []syntax.WordPart{
		&syntax.Lit{Value: idx},
	}}
}

func (r *Runner) arrayExpandOnceEnabled() bool {
	opt, _ := r.bashOptByName("array_expand_once")
	return opt != nil && *opt
}

func (r *Runner) arrayExpandOnceIndexedSubscriptErr(idx string) bool {
	if !r.arrayExpandOnceEnabled() {
		return false
	}
	tok := expand.ArithmCmdSubstToken(idx)
	if tok == "" {
		return false
	}
	r.expandErr(&expand.ArithmError{
		Text: idx,
		Err:  fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", tok),
	})
	r.exit.code = 1
	return true
}

func (r *Runner) arrayExpandOnceIndexExprErr(index syntax.ArithmExpr) bool {
	if !r.arrayExpandOnceEnabled() {
		return false
	}
	w, ok := index.(*syntax.Word)
	if !ok {
		return false
	}
	idx, ok := r.assocAssignKeyLiteral(w.Parts)
	if !ok {
		return false
	}
	return r.arrayExpandOnceIndexedSubscriptErr(idx)
}

// setExportedVarString sets a scalar variable and marks it exported, so it
// appears in the environment of child processes / `env`. bash keeps PWD and
// OLDPWD exported across every cd; setVarString alone leaves them unexported.
func (r *Runner) setExportedVarString(name, value string) {
	r.setVar(name, expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: value})
}

func (r *Runner) setIgnoreEOFOption(enable bool) {
	if r.noOpSetState == nil {
		r.noOpSetState = make(map[string]bool)
	}
	r.noOpSetState["ignoreeof"] = enable
	// When `-o ignoreeof` is applied via [Params] at construction time (before
	// the first [Runner.Reset]), the variable environment is not set up yet, so
	// the IGNOREEOF assignment below would nil-panic. Record the option state
	// only; Reset re-applies the variable once writeEnv exists.
	if r.writeEnv == nil {
		return
	}
	r.settingIgnoreEOFOption = true
	if enable {
		r.setVarString("IGNOREEOF", "10")
	} else {
		r.setVarString("IGNOREEOF", "0")
	}
	r.settingIgnoreEOFOption = false
}

func validAssignName(name string) bool {
	if syntax.ValidName(name) {
		return true
	}
	base, _, ok := splitArrayRef(name)
	return ok && syntax.ValidName(base)
}

func validNameRefTarget(name string) bool {
	if syntax.ValidName(name) {
		return true
	}
	base, idx, ok := splitArrayRef(name)
	return ok && idx != "" && syntax.ValidName(base)
}

// bashDeclareQuote formats v the way bash's `declare -p` does: bare
// when safe, double-quoted otherwise, falling back to ANSI-C $'...'
// when v contains characters that double quotes can't represent
// (control bytes, NULs, non-UTF-8 bytes, etc.).
// bashSetQuote formats `v` the way bash's `set` (no args) builtin
// does: no quotes for fully-safe strings, backslash-escape for a
// single shell-special character, single-quotes for everything else
// (with `'` itself rendered as `'\”`). Differs from
// [bashDeclareQuote] (which always double-quotes) and from
// [syntax.Quote] (which always quotes `#`).
func bashSetQuote(v string) string {
	if v == "" {
		return "''"
	}
	if !bashSetNeedsQuoting(v) {
		return v
	}
	// Bash 5.3's `set` falls back to backslash-escape only for the
	// single-quote character itself — everything else uses single
	// quotes. Empty result from this if-block falls through to the
	// general single-quoting case below.
	if v == "'" {
		return `\'`
	}
	// General case: single-quote, with embedded `'` → `'\''`.
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// bashSetNeedsQuoting reports whether a value contains any character
// bash's `set` would treat specially. The leading-only chars `#` and
// `~` are special only at position 0.
func bashSetNeedsQuoting(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case ' ', '\t', '\n', '\r', '\v', '\f',
			'"', '\'', '`', '\\', '$',
			';', '|', '&', '<', '>', '(', ')',
			'{', '}', '[', ']', '*', '?', '!':
			return true
		case '#', '~', '=':
			if i == 0 {
				return true
			}
		}
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

func bashDeclareQuote(v string) string {
	if hasNonPrintable(v) {
		q, err := syntax.Quote(v, syntax.LangBash)
		if err == nil {
			// GNU Bash's declaration/export format spells ESC as \E.
			// Quote's general-purpose octal spelling round-trips too, but
			// consumers of `export -p` also depend on Bash's exact format.
			// Walk escapes so a literal backslash followed by 033 stays literal.
			if strings.ContainsRune(v, '\x1b') {
				var b strings.Builder
				for i := 0; i < len(q); i++ {
					if q[i] == '\\' && i+1 < len(q) {
						if strings.HasPrefix(q[i:], `\033`) {
							b.WriteString(`\E`)
							i += 3
							continue
						}
						b.WriteByte(q[i])
						i++
					}
					b.WriteByte(q[i])
				}
				return b.String()
			}
			return q
		}
	}
	// Mirror bash's "always double-quote inside the array literal".
	// Escape the four characters that have special meaning in double-
	// quoted bash: ", \, $, ` — leaving everything else literal so the
	// output matches `declare -p` byte-for-byte.
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

func hasNonPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// aliasValue returns the textual form of an alias as bash stores it in
// BASH_ALIASES and displays it via `alias`/`type`: the literal body
// verbatim, including any trailing whitespace from the definition.
func aliasValue(als alias) string {
	return als.text
}

// parseAliasBody parses an alias body string into an alias struct. Bash
// stores alias bodies as TEXT and only re-parses at expansion time, so
// bodies that don't parse standalone (a bare keyword, unclosed quotes,
// a leading comment) fall back to per-word or raw text storage. The
// `alias` builtin and BASH_ALIASES[...]= assignments share this path so
// both populate r.alias identically.
func parseAliasBody(src string) alias {
	als := alias{
		text:  src,
		blank: strings.TrimRight(src, " \t") != src,
	}
	if strings.HasPrefix(strings.TrimLeft(src, " \t"), "#") {
		als.raw = src
		return als
	}
	parser := syntax.NewParser()
	file, perr := parser.Parse(strings.NewReader(src), "")
	if perr == nil {
		if len(file.Stmts) == 1 {
			if ce, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(ce.Assigns) == 0 && file.Stmts[0].Redirs == nil {
				als.args = ce.Args
			}
		}
		if als.args == nil {
			als.file = file
		}
		return als
	}
	// Stmt-parse failed — try the per-word path. If even that fails,
	// store the raw text so it round-trips unchanged.
	var words []*syntax.Word
	var werr error
	for w, e := range parser.WordsSeq(strings.NewReader(src)) {
		if e != nil {
			werr = e
			break
		}
		words = append(words, w)
	}
	if werr != nil {
		als.raw = src
		return als
	}
	als.args = words
	return als
}

func validAliasName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\$")
}

// terminalSize probes stdin/stdout/stderr for a terminal and returns
// the first valid (cols, rows). Returns (0, 0) if none of them is a
// terminal — callers use that to leave $COLUMNS / $LINES empty.
func terminalSize() (cols, rows int) {
	for _, fd := range []int{
		int(os.Stdin.Fd()),
		int(os.Stdout.Fd()),
		int(os.Stderr.Fd()),
	} {
		if c, r, err := term.GetSize(fd); err == nil {
			return c, r
		}
	}
	return 0, 0
}

func terminalWidth() int {
	c, _ := terminalSize()
	return c
}

// restrictedVars are the variables a restricted shell freezes so that the
// path used to find commands cannot be redirected. Bash marks each one
// readonly when restricted mode turns on; every later assignment, declare,
// local, export, or unset then reports the standard "readonly variable"
// error (with the usual builtin-name prefix where bash adds one).
var restrictedVars = [...]string{"PATH", "SHELL", "ENV", "BASH_ENV"}

// markRestrictedVarsReadonly freezes the restricted-shell variables, matching
// bash's restricted mode. It is idempotent and a no-op until r.writeEnv has
// been initialized (Reset re-applies it once the scope exists).
func (r *Runner) markRestrictedVarsReadonly() {
	if r.writeEnv == nil {
		return
	}
	for _, name := range restrictedVars {
		vr := r.lookupVar(name)
		if vr.ReadOnly {
			continue
		}
		vr.ReadOnly = true
		r.writeEnv.Set(name, vr)
	}
}

func (r *Runner) setVar(name string, vr expand.Variable) {
	if decl, ok := r.hostedDeclaration(name); ok && decl.Constant {
		r.bashPPDeclRefused = true
		r.refuseConstantAssignment(name)
		return
	}
	// An ordinary shell assignment to a name a `var` declaration bound writes
	// THROUGH to that binding rather than creating a shell variable beside
	// it. Two stores for one name is the failure this whole file exists to
	// prevent: expansion would read the typed cell while an external command
	// read the shell one.
	if r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(name); cell != nil {
			if cell.viewConstant() {
				// Diagnosed outside the guard: reporting writes to stderr,
				// which may block, and a guarded region must not.
				r.refuseConstantAssignment(name)
				return
			}
			// The attribute merge reads the cell's current header and then
			// republishes it together with the channel authority, so a cell an
			// interpreted goroutine can also reach is read and written under
			// one guard; see bashpp_cell_share.go. The region only moves
			// fields.
			cell.lock()
			defer cell.unlock()
			if vr.Kind == expand.KeepValue {
				cell.vr.Exported = cell.vr.Exported || vr.Exported
				cell.vr.ReadOnly = cell.vr.ReadOnly || vr.ReadOnly
				if cell.object != nil && cell.vr.ReadOnly {
					cell.object.readonly = true
				}
				return
			}
			// Attributes belong to the declaration, not to the assignment:
			// a binding which shadows an exported shell variable keeps
			// reaching child processes after `x=5`.
			vr.Exported = vr.Exported || cell.vr.Exported
			vr.ReadOnly = cell.vr.ReadOnly
			cell.vr = vr
			// Ordinary string/value assignment revokes any channel authority.
			// The few typed direct-copy paths explicitly reattach provenance
			// after storing the copied visible value.
			cell.channel, cell.channelOwner = nil, nil
			if cell.object != nil && vr.ReadOnly {
				cell.object.readonly = true
			}
			return
		}
	}
	if base, idx, ok := splitArrayRef(name); ok && syntax.ValidName(base) && vr.Kind == expand.String {
		r.setVarWithIndex(r.lookupVar(base), base, r.arrayTargetIndex(idx), vr, false)
		return
	}
	if name == "RANDOM" && vr.IsSet() {
		if seed, err := strconv.ParseInt(vr.String(), 10, 64); err == nil {
			r.randomSeeded = true
			r.randomSeed = uint32(seed)
		}
		return
	}
	if name == "IGNOREEOF" && vr.IsSet() && !r.settingIgnoreEOFOption {
		if r.noOpSetState == nil {
			r.noOpSetState = make(map[string]bool)
		}
		r.noOpSetState["ignoreeof"] = true
		if vr.Kind == expand.String && vr.Str == "0" {
			vr.Str = "10"
		}
	}
	if r.opts[optAllExport] {
		vr.Exported = true
	}
	// Carry forward case-conversion attributes from any prior
	// declaration of the same name (`declare -u foo` followed by
	// `foo=value`), and fold the value through them so the stored
	// value already reflects the attribute.
	prev := r.lookupVar(name)
	if prev.Upper || prev.Lower || prev.Capitalize {
		vr.Upper, vr.Lower, vr.Capitalize = prev.Upper, prev.Lower, prev.Capitalize
	}
	applyCaseAttr(&vr)
	// BASH_ARGV0 is writable. Update r.argv0 so subsequent reads of
	// $0 and $BASH_ARGV0 see the new value. r.filename stays as-is so
	// error messages keep referring to the original script.
	if name == "BASH_ARGV0" && vr.IsSet() {
		r.argv0 = vr.Str
	}
	// POSIXLY_CORRECT switches bash into POSIX mode whenever it is
	// set (and back out when unset). Mirror that here so subsequent
	// POSIX-gated behaviour (special-builtin assignment persistence,
	// etc.) kicks in without an explicit `set -o posix`.
	if name == "POSIXLY_CORRECT" {
		r.setPosixMode(vr.IsSet() && vr.Str != "")
	}
	// BASH_XTRACEFD: when assigned, validate that the file
	// descriptor is open and writable; bash 5.3 emits
	// "BASH_XTRACEFD: <fd>: invalid value for trace file
	// descriptor" otherwise and refuses to use it.
	if name == "BASH_XTRACEFD" && vr.IsSet() && vr.Str != "" {
		if fd, err := strconv.Atoi(vr.Str); err == nil && fd >= 0 {
			if _, ok := r.fdTable[fd]; !ok {
				r.errf("%sBASH_XTRACEFD: %s: invalid value for trace file descriptor\n",
					r.bashErrPrefix(r.curStmtPos), vr.Str)
				return
			}
		}
	}
	// Bash refuses to convert between indexed and associative arrays
	// via the declare family; the variable keeps its prior kind. The
	// failure is attributed to the builtin, with an extra
	// function-attributed line when it came from an array literal
	// assignment inside a function (matching bash 5.3's double report).
	if r.declAssignContext && prev.Declared() {
		var convErr string
		switch {
		case prev.Kind == expand.Indexed && vr.Kind == expand.Associative:
			convErr = "cannot convert indexed to associative array"
		case prev.Kind == expand.Associative && vr.Kind == expand.Indexed:
			convErr = "cannot convert associative to indexed array"
		}
		if convErr != "" {
			if r.setVarArrayLiteral && len(r.callStack) > 0 {
				r.errf("%s%s: %s: %s\n", r.bashErrPrefix(r.curStmtPos),
					r.callStack[len(r.callStack)-1].funcName, name, convErr)
			}
			// A top-level array-literal assignment (`declare -a A=(...)`)
			// that fails to convert is attributed by bash 5.3 to the
			// variable alone, with no builtin prefix; the builtin-prefixed
			// line only accompanies the function-attributed report.
			// When the assignment runs from an `eval` body, bash instead
			// attributes the failure to the `eval` builtin.
			if r.setVarArrayLiteral && len(r.callStack) == 0 && r.evalExec == 0 {
				r.errf("%s%s: %s\n", r.bashErrPrefix(r.curStmtPos), name, convErr)
			} else {
				builtin := r.setVarFromBuiltin
				if builtin == "" {
					builtin = "declare"
				}
				if r.evalExec > 0 {
					builtin = "eval"
				}
				r.errf("%s%s: %s: %s\n", r.bashErrPrefix(r.curStmtPos), builtin, name, convErr)
			}
			r.exit.code = 1
			return
		}
		// `declare -A name` (no value) on an existing scalar converts
		// it like bash's convert_var_to_assoc: the old value becomes
		// element "0", and the resulting table has 128 hash buckets
		// (assoc_create(0)) rather than a fresh declare -A's 1024,
		// which `declare -p` key ordering reflects.
		if vr.Kind == expand.Associative && vr.Map == nil && vr.Set && prev.Kind == expand.String {
			vr.Map = map[string]string{"0": vr.Str}
			vr.Str = ""
			vr.AssocBuckets = 128
		}
	}
	// `local x` (no value) creates a fresh unset local; bash only
	// inherits the previous scope's value with shopt localvar_inherit
	// or when x is bound in the call's temporary environment
	// (`x=4 foo` + `local -i x` inside foo sees 4).
	// Explicitly-requested attributes like `-i`/`-u` are kept, but the
	// parent's value is not.
	if vr.Local && vr.Kind == expand.KeepValue &&
		!r.tempEnv[name] &&
		!r.bashShoptEnabled("localvar_inherit") {
		if ol, ok := r.writeEnv.(*overlayEnviron); ok && ol.funcScope && !ol.holdsLocally(name) {
			vr = expand.Variable{
				Local:      true,
				Integer:    vr.Integer,
				Upper:      vr.Upper,
				Lower:      vr.Lower,
				Capitalize: vr.Capitalize,
			}
		}
	}
	if err := r.writeEnv.Set(name, vr); err != nil {
		// Bash 5.3 attribution: syntax-level assignment failures
		// inside a function are prefixed with the function name
		// (`<file>: line N: <fn>: <var>: readonly variable`);
		// failures from a declare-family builtin's array-conversion
		// path (`readonly -a 'name=value'`) are prefixed with the
		// builtin name. Plain string-form assignments via the same
		// builtins get no extra prefix.
		var inner string
		switch {
		case r.setVarFromBuiltin != "":
			inner = r.setVarFromBuiltin + ": "
		case r.setVarStringParsed:
			// String-parsed assignment without array flag: no extra
			// prefix beyond `<file>: line N: <var>:`.
		case r.setVarArrayLiteral && len(r.callStack) > 0:
			// Syntax-level array-literal assignment inside a
			// function: bash attributes to the function name.
			inner = r.callStack[len(r.callStack)-1].funcName + ": "
		}
		// A readonly array append/element store through a nameref is
		// reported by bash under the nameref name as written, not the
		// resolved target (see r.assignNamerefName).
		displayName := name
		if r.assignNamerefName != "" {
			displayName = r.assignNamerefName
		}
		// An array-literal assignment failing on a readonly variable
		// through `local` is reported twice by bash: once for the
		// failed assignment, once from the builtin. `readonly`/`export`
		// and declare-family forms report only once (see attr.tests).
		if r.setVarFromBuiltin == "local" &&
			(vr.Kind == expand.Indexed || vr.Kind == expand.Associative) &&
			strings.Contains(err.Error(), "readonly") {
			r.errf("%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), displayName, err)
		}
		r.errf("%s%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), inner, displayName, err)
		r.exit.code = 1
		return
	}
	// Bash invalidates remembered command locations on every successful
	// PATH assignment, including a temporary assignment before a command.
	if name == "PATH" {
		clear(r.cmdHashTable)
	}
}

func (r *Runner) rejectDeclareConversion(name string, prev, vr expand.Variable) bool {
	if !r.declAssignContext || !prev.Declared() {
		return false
	}
	var convErr string
	switch {
	case prev.Kind == expand.Indexed && vr.Kind == expand.Associative:
		convErr = "cannot convert indexed to associative array"
	case prev.Kind == expand.Associative && vr.Kind == expand.Indexed:
		convErr = "cannot convert associative to indexed array"
	}
	if convErr == "" {
		return false
	}
	if r.setVarArrayLiteral && len(r.callStack) > 0 {
		r.errf("%s%s: %s: %s\n", r.bashErrPrefix(r.curStmtPos),
			r.callStack[len(r.callStack)-1].funcName, name, convErr)
	}
	// A top-level array-literal assignment (`declare -a A=(...)`) that
	// fails to convert is attributed by bash 5.3 to the variable alone,
	// with no builtin prefix; the builtin-prefixed line only accompanies
	// the function-attributed report.
	if r.setVarArrayLiteral && len(r.callStack) == 0 {
		r.errf("%s%s: %s\n", r.bashErrPrefix(r.curStmtPos), name, convErr)
		r.exit.code = 1
		return true
	}
	builtin := r.setVarFromBuiltin
	if builtin == "" {
		builtin = "declare"
	}
	r.errf("%s%s: %s: %s\n", r.bashErrPrefix(r.curStmtPos), builtin, name, convErr)
	r.exit.code = 1
	return true
}

func literalBadIndexedAssignSubscript(index syntax.ArithmExpr) (string, bool) {
	w, ok := index.(*syntax.Word)
	if !ok || len(w.Parts) != 1 {
		return "", false
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	switch lit.Value {
	case "", "*", "@":
		return lit.Value, true
	default:
		return "", false
	}
}

func escapedQuotedAssignIndex(expr syntax.ArithmExpr) (string, bool) {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return "", false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(lit.Value, `\"`) || !strings.HasSuffix(lit.Value, `\"`) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(lit.Value, `\"`), `\"`), true
}

// bashIndexArithmSyntaxError reports bash's diagnostic for a subscript whose
// text is not a complete arithmetic expression, or "" when it is fine.
//
// The parser deliberately accepts such a subscript (`a[b[c]d]=e`): bash scans
// to the matching bracket and only complains when it EVALUATES the subscript.
// Reproduce that evaluation by re-parsing the text the way $(( )) is parsed,
// which — unlike [syntax.Parser.Arithmetic] — refuses trailing junk. bash's
// error token is the unparsed remainder from the failure point, and the
// wording splits on whether an operand or an operator was the problem.
func bashIndexArithmSyntaxError(text string) string {
	const prefix = "$(("
	src := prefix + text + "))"
	_, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err == nil {
		return ""
	}
	pe, ok := err.(syntax.ParseError)
	if !ok {
		return ""
	}
	off := int(pe.Pos.Offset()) - len(prefix)
	token := text
	if off >= 0 && off < len(text) {
		token = text[off:]
	}
	if strings.Contains(pe.Text, "not a valid arithmetic operator") {
		return fmt.Sprintf("%s: arithmetic syntax error in expression (error token is %q)", text, token)
	}
	return fmt.Sprintf("%s: arithmetic syntax error: operand expected (error token is %q)", text, token)
}

func (r *Runner) arithmCompoundArrayIndex(expr syntax.ArithmExpr) (int, bool) {
	rawText := ""
	if word, ok := expr.(*syntax.Word); ok && len(word.Parts) == 1 {
		if _, ok := word.Parts[0].(*syntax.Lit); ok {
			rawText = word.Lit()
			if !strings.ContainsAny(rawText, "'#") {
				if msg := bashIndexArithmSyntaxError(rawText); msg != "" {
					prefix := r.filename
					if prefix == "" {
						prefix = "bash"
					}
					err := fmt.Errorf("%s: line %d: %s", prefix, r.curStmtPos.Line(), msg)
					r.lastArithErr = err
					r.expandErr(err)
					return 0, false
				}
			}
			if strings.ContainsAny(rawText, "'#") {
				if strings.Contains(rawText, "#") {
					err := r.rawAssignIndexArithError(rawText, fmt.Errorf("not a valid arithmetic operator"))
					r.lastArithErr = err
					r.expandErr(err)
					return 0, false
				}
				if parsed, perr := syntax.NewParser(syntax.Variant(syntax.LangBash)).Arithmetic(strings.NewReader(rawText)); perr == nil {
					expr = parsed
				} else {
					err := r.rawAssignIndexArithError(rawText, perr)
					r.lastArithErr = err
					r.expandErr(err)
					return 0, false
				}
			}
		}
	} else if text := r.arithmSourceText(expr, false); strings.ContainsAny(text, "'#") {
		rawText = text
		if strings.Contains(rawText, "#") {
			err := r.rawAssignIndexArithError(rawText, fmt.Errorf("not a valid arithmetic operator"))
			r.lastArithErr = err
			r.expandErr(err)
			return 0, false
		}
		if parsed, perr := syntax.NewParser(syntax.Variant(syntax.LangBash)).Arithmetic(strings.NewReader(text)); perr == nil {
			expr = parsed
		} else {
			err := r.rawAssignIndexArithError(text, perr)
			r.lastArithErr = err
			r.expandErr(err)
			return 0, false
		}
	}
	n, err := expand.Arithm(r.ecfg, expr)
	if err == nil {
		r.lastArithErr = nil
		return n, true
	}
	if rawText != "" && strings.Contains(err.Error(), "not a valid arithmetic operator") {
		text := strings.TrimSpace(rawText)
		token := text
		if i := strings.IndexByte(text, '#'); i >= 0 {
			token = text[i:]
		}
		prefix := r.filename
		if prefix == "" {
			prefix = "bash"
		}
		err = fmt.Errorf("%s: line %d: %s: arithmetic syntax error: invalid arithmetic operator (error token is %q)",
			prefix, r.curStmtPos.Line(), text, token)
		r.lastArithErr = err
		r.expandErr(err)
		return n, false
	}
	if r.bashCompatErrors {
		if rawText != "" {
			err = r.bashRawAssignIndexArithError(expr, rawText, err)
			r.lastArithErr = err
			r.expandErr(err)
			return n, false
		}
		if arithErr, arithExpr := innermostArithmError(err); arithErr != nil {
			if arithExpr != nil {
				expr = arithExpr
			}
			exprText := arithErr.Text
			if exprText == "" {
				exprText = r.arithmSourceText(expr, false)
			}
			msg := arithErr.Err.Error()
			if strings.Contains(msg, "not a valid arithmetic operator") {
				token := strings.TrimSpace(exprText)
				if i := strings.LastIndexByte(token, ' '); i >= 0 {
					token = token[i+1:]
				}
				prefix := r.filename
				if prefix == "" {
					prefix = "bash"
				}
				line := int(r.curStmtPos.Line())
				err = fmt.Errorf("%s: line %d: %s: arithmetic syntax error in expression (error token is %q)",
					prefix, line, strings.TrimSpace(exprText), token)
			} else {
				err = r.bashArithmError(expr, arithErr.Err, false, arithErr.Text)
			}
		} else {
			err = r.bashArithmError(expr, err, false, "")
		}
	}
	r.lastArithErr = err
	r.expandErr(err)
	return n, false
}

func (r *Runner) bashRawAssignIndexArithError(expr syntax.ArithmExpr, rawText string, err error) error {
	if arithErr, _ := innermostArithmError(err); arithErr != nil {
		err = arithErr.Err
	}
	text := strings.TrimSpace(rawText)
	msg := err.Error()
	if strings.Contains(msg, "arithmetic syntax error: operand expected") &&
		strings.Contains(msg, "error token is") {
		if start := strings.Index(msg, "error token is \""); start >= 0 {
			tokenStart := start + len("error token is \"")
			if tokenEnd := strings.IndexByte(msg[tokenStart:], '"'); tokenEnd >= 0 {
				token := strings.TrimSpace(msg[tokenStart : tokenStart+tokenEnd])
				msg = msg[:tokenStart] + token + msg[tokenStart+tokenEnd:]
			}
		}
	}
	prefix := r.filename
	if prefix == "" {
		prefix = "bash"
	}
	line := int(r.curStmtPos.Line())
	if line == 0 && expr != nil {
		line = int(expr.Pos().Line())
	}
	return fmt.Errorf("%s: line %d: %s: %s", prefix, line, text, msg)
}

func (r *Runner) rawAssignIndexArithError(rawText string, err error) error {
	text := strings.TrimSpace(rawText)
	token := text
	if i := strings.IndexByte(text, '#'); i >= 0 {
		token = text[i:]
	}
	prefix := r.filename
	if prefix == "" {
		prefix = "bash"
	}
	if !strings.Contains(err.Error(), "not a valid arithmetic operator") {
		return fmt.Errorf("%s: line %d: %s: %s", prefix, r.curStmtPos.Line(), text, err)
	}
	return fmt.Errorf("%s: line %d: %s: arithmetic syntax error: invalid arithmetic operator (error token is %q)",
		prefix, r.curStmtPos.Line(), text, token)
}

func (r *Runner) setVarWithIndex(prev expand.Variable, name string, index syntax.ArithmExpr, vr expand.Variable, appendValue bool) {
	if vr.Kind == expand.String && index == nil {
		// When assigning a string to an array, fall back to the
		// zero value for the index.
		switch prev.Kind {
		case expand.Indexed:
			index = &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: "0"},
			}}
		case expand.Associative:
			index = &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: "0"},
			}}
		}
	}
	if index == nil {
		r.setVar(name, vr)
		return
	}
	if prev.Kind == expand.NameRef && prev.Str == "" {
		r.errf("%s`': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos))
		r.exit.code = 1
		return
	}
	if idx, bad := literalBadIndexedAssignSubscript(index); bad && prev.Kind != expand.Associative {
		r.errf("%s%s[%s]: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), name, idx)
		r.exit.code = 1
		return
	}

	// from the syntax package, we know that value must be a string if index
	// is non-nil; nested arrays are forbidden.
	valStr := vr.Str

	switch prev.Kind {
	case expand.Associative:
		// if the existing variable is already an AssocArray, try our
		// best to convert the key to a string
		if index == nil {
			r.errf("%s%s: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), name)
			r.exit.code = 1
			return
		}
		k, ok := r.assocAssignIndexKey(index)
		if !ok {
			return
		}
		if name == "BASH_ALIASES" || name == "BASH_CMDS" {
			if w, ok := index.(*syntax.Word); ok {
				k = r.literal(w)
			}
		}
		if name == "BASH_ALIASES" {
			if !validAliasName(k) {
				r.errf("%s`%s': invalid alias name\n", r.bashErrPrefix(r.curStmtPos), k)
				r.exit.code = 1
				return
			}
			// BASH_ALIASES is a live view of the alias table: writing an
			// element creates/updates an alias, exactly like `alias k=v`.
			// Reads regenerate the map from r.alias, so storing here keeps
			// the two in sync.
			if r.alias == nil {
				r.alias = make(map[string]alias)
			}
			als := parseAliasBody(valStr)
			als.defLine = r.aliasDefLine(int(r.curStmtPos.Line()))
			r.alias[k] = als
			return
		}
		if name == "BASH_CMDS" {
			if info, err := os.Stat(valStr); err == nil && info.IsDir() {
				r.errf("%s%s: Is a directory\n", r.bashErrPrefix(r.curStmtPos), valStr)
				r.exit.code = 1
				return
			}
			if r.opts[optRestricted] {
				if strings.Contains(valStr, "/") {
					r.errf("%s%s: restricted\n", r.bashErrPrefix(r.curStmtPos), valStr)
					r.exit.code = 1
					return
				}
				if _, err := LookPathDir(r.Dir, r.writeEnv, valStr); err != nil {
					r.errf("%s%s: not found\n", r.bashErrPrefix(r.curStmtPos), valStr)
					r.exit.code = 1
					return
				}
			}
			if r.cmdHashTable == nil {
				r.cmdHashTable = make(map[string]cmdHashEntry)
			}
			r.cmdHashTable[k] = cmdHashEntry{path: valStr, restricted: r.opts[optRestricted]}
			return
		}

		// TODO: only clone when inside a subshell and getting a var from outside for the first time
		prev.Map = maps.Clone(prev.Map)
		if prev.Map == nil {
			prev.Map = make(map[string]string)
		}
		if prev.Integer {
			valStr = r.integerArrayValue(valStr)
		}
		prev.Map[k] = valStr
		// A bare associative array expands like key "0"; assigning any
		// other key does not make `${A-default}` see A as set.
		if k == "0" {
			prev.Set = true
		}
		r.setVar(name, prev)
		return
	}
	if text, ok := escapedQuotedAssignIndex(index); ok {
		quoted := `"` + text + `"`
		r.errf("%s%s: arithmetic syntax error: operand expected (error token is \"%s\")\n",
			r.bashErrPrefix(r.curStmtPos), quoted, quoted)
		r.exit.code = 1
		return
	}
	if r.arrayExpandOnceIndexExprErr(index) {
		return
	}
	k, ok := r.arithmCompoundArrayIndex(index)
	if !ok {
		r.exit.code = 1
		return
	}
	// The arithmetic subscript can itself mutate the target array, as
	// in `a[a[0]=1]=X`. Re-read the target after evaluating the index
	// so the final element write merges with those side effects instead
	// of replacing them with the pre-evaluation snapshot.
	prev = r.lookupVar(name)
	// Build a working indexed copy of the variable; SetIndexed below chooses
	// dense or sparse storage so a huge subscript does not balloon the slice.
	work := prev
	work.Kind = expand.Indexed
	work.Str = ""
	switch prev.Kind {
	case expand.String:
		work.List = []string{prev.Str}
		work.ListSet = nil
		work.ListMap = nil
	case expand.Indexed:
		// TODO: only clone when inside a subshell and getting a var from outside for the first time
		work.List = slices.Clone(prev.List)
		work.ListSet = prev.CloneListSet()
		work.ListMap = prev.CloneListMap()
	default:
		work.List = nil
		work.ListSet = nil
		work.ListMap = nil
	}
	if k < 0 {
		// Bash 5.3 accepts negative indices as offsets from the
		// end of the array: `a[-1]` targets the last element.
		k = indexedNegativeOffset(work, k)
		if k < 0 {
			idxText := r.arithmSourceText(index, false)
			if idxText == "" {
				idxText = strconv.Itoa(k)
			}
			r.errf("%s%s[%s]: bad array subscript\n", r.bashErrPrefix(r.curStmtPos), name, idxText)
			r.exit.code = 1
			return
		}
	}
	if name == "DIRSTACK" {
		if k >= len(r.dirStack) {
			return
		}
		r.dirStack[len(r.dirStack)-1-k] = valStr
		return
	}
	if appendValue {
		cur := work.IndexedElem(k)
		if prev.Integer {
			c, _ := strconv.Atoi(r.integerArrayValue(cur))
			rhs, _ := strconv.Atoi(r.integerArrayValue(valStr))
			valStr = strconv.Itoa(c + rhs)
		} else {
			valStr = cur + valStr
		}
	}
	work.SetIndexed(k, valStr)
	prev = work
	// Assigning element 0 makes the array variable "set": `declare -a B` leaves
	// B declared-but-unset, and `B[0]=v` must flip it so a later `${B[0]}` (or
	// bare `${B}`/`${B-default}`, which read element 0) under `set -u` reads it
	// instead of falsely erroring "unbound variable" (arrays via `A=()` are
	// already set — this makes both forms consistent). Mirrors the associative
	// rule above: assigning a non-zero index does NOT make `${B-default}` see B
	// as set, so gate on index 0.
	if k == 0 {
		prev.Set = true
	}
	r.setVar(name, prev)
}

func (r *Runner) arrayElemAsScalar(idx string, vr expand.Variable) expand.Variable {
	elem := expand.Variable{
		Kind:       expand.String,
		Integer:    vr.Integer,
		Upper:      vr.Upper,
		Lower:      vr.Lower,
		Capitalize: vr.Capitalize,
	}
	switch vr.Kind {
	case expand.Indexed:
		n64, err := r.arithFromString(idx)
		if err != nil {
			return elem
		}
		n := int(n64)
		if n < 0 {
			n = indexedNegativeOffset(vr, n)
		}
		if vr.IndexedSet(n) {
			elem.Set = true
			elem.Str = vr.IndexedElem(n)
		}
	case expand.Associative:
		if vr.Map != nil {
			idx = r.assocArrayElemKey(idx)
			if s, ok := vr.Map[idx]; ok {
				elem.Set = true
				elem.Str = s
			}
		}
	default:
		if idx == "0" && vr.IsSet() {
			elem.Set = true
			elem.Str = vr.String()
		}
	}
	return elem
}

func (r *Runner) assocArrayElemKey(idx string) string {
	w, ok := r.arrayTargetIndex(idx).(*syntax.Word)
	if !ok {
		return idx
	}
	return r.assocAssignKey(w)
}

func (r *Runner) integerArrayValue(s string) string {
	if s == "" {
		return "0"
	}
	expr, perr := syntax.NewParser().Arithmetic(strings.NewReader(s))
	if perr != nil || expr == nil {
		return "0"
	}
	v, _ := expand.Arithm(r.ecfg, expr)
	return strconv.Itoa(v)
}

func (r *Runner) setFunc(name string, body *syntax.Stmt) {
	delete(r.bashPPAgenticFuncs, name)
	delete(r.bashPPDecoratedFuncs, name)
	if r.Funcs == nil {
		r.Funcs = make(map[string]*syntax.Stmt, 4)
	}
	r.Funcs[name] = body
	if r.funcSources == nil {
		r.funcSources = make(map[string]string, 4)
	}
	r.funcSources[name] = r.filename
	// Capture the lexical environment the definition was written in. The
	// snapshot shares cells but not the enclosing block's name map, so the
	// function sees later WRITES to what it closed over and never sees later
	// DECLARATIONS beside it; see [bashPPScope.snapshot].
	if r.bashPPScope != nil {
		r.bashPPUnshareFuncScopes()
		if r.bashPPFuncScopes == nil {
			r.bashPPFuncScopes = make(map[string]*bashPPScope, 4)
		}
		r.bashPPFuncScopes[name] = r.bashPPScope.snapshot()
	}
}

// wordLooksLikeAssign mirrors bash's W_ASSIGNMENT check for the first
// word of a compound assoc assignment: `name=...` (or `name+=...`)
// words keep the subscript-required error path rather than enabling
// key/value pair mode.
func wordLooksLikeAssign(w *syntax.Word) bool {
	if w == nil || len(w.Parts) == 0 {
		return false
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok {
		return false
	}
	eq := strings.IndexByte(lit.Value, '=')
	if eq <= 0 {
		return false
	}
	return syntax.ValidName(strings.TrimSuffix(lit.Value[:eq], "+"))
}

func bashAssocAssignKey(s string, quoted bool) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"', '\'', '$', '`', '\\', '[':
				i++
			case ']', ')':
				if !quoted {
					i++
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (r *Runner) assocAssignKey(w *syntax.Word) string {
	key := r.assocAssignKeyRaw(w)
	// A subscript whose leading literal is an unquoted tilde introduces a
	// tilde prefix (`aa[~/key]=v`); bash tilde-expands it just like the
	// rest of the assignment word.
	if len(w.Parts) > 0 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(lit.Value, "~") {
			key = expand.ExpandTildeAssign(r.ecfg, key)
		}
	}
	return key
}

func (r *Runner) assocAssignIndexKey(expr syntax.ArithmExpr) (string, bool) {
	if w, ok := expr.(*syntax.Word); ok {
		return r.assocAssignKey(w), true
	}
	key := r.arithmSourceText(expr, false)
	if key == "" {
		var b strings.Builder
		syntax.NewPrinter().Print(&b, expr)
		key = b.String()
	}
	return key, key != ""
}

func (r *Runner) assocAssignKeyRaw(w *syntax.Word) string {
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok {
			return bashAssocAssignKey(lit.Value, false)
		}
	}
	if key, ok := r.assocAssignKeyLiteral(w.Parts); ok {
		return bashAssocAssignKey(key, assocAssignKeyQuoted(w.Parts))
	}
	return r.literal(w)
}

func (r *Runner) assocAssignKeyLiteral(parts []syntax.WordPart) (string, bool) {
	var b strings.Builder
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			s, ok := r.assocAssignKeyLiteral(part.Parts)
			if !ok {
				return "", false
			}
			b.WriteString(s)
		case *syntax.ParamExp:
			if part.Param == nil || part.NestedParam != nil || part.Index != nil ||
				part.Slice != nil || part.Repl != nil || part.Exp != nil ||
				part.Length || part.Width || part.Excl || part.Names != 0 {
				return "", false
			}
			b.WriteString(r.envGet(part.Param.Value))
		default:
			return "", false
		}
	}
	return b.String(), true
}

// TODO: make assignVal and [setVar] consistent with the [expand.WriteEnviron] interface

func bashIntegerAssignArithErr(src string, err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "must be followed by an expression") {
		return err
	}
	token := strings.TrimSpace(src)
	if idx := strings.LastIndexAny(src, "+-*/%<>=!&|^?:,"); idx >= 0 {
		token = strings.TrimSpace(src[idx:])
	}
	return fmt.Errorf("arithmetic syntax error: operand expected (error token is %q)", token)
}

func (r *Runner) assignVal(name string, prev expand.Variable, as *syntax.Assign, valType string) (string, expand.Variable) {
	// `declare -n NAME=target` retargets the nameref itself —
	// don't dereference the existing nameref first or we'd
	// overwrite the *target* (e.g. `typeset -n fee=flow` followed
	// by `typeset -n fee=flip` should leave fee→flip, not
	// flow=flip).
	if valType != "-n" {
		if prev.Kind == expand.NameRef && prev.Str == "" && as.Index == nil {
			valType = "-n"
		}
	}
	// preserveArrayKind forces the array-element-0 store below even
	// outside a declare-family context — used when a circular nameref
	// rebinds onto an existing global array (see the max-nameref-depth
	// branch), where bash keeps the array and writes element [0].
	preserveArrayKind := false
	if valType != "-n" {
		// A self-referencing local nameref (`typeset -n v=v` inside a
		// function) loops back to itself during assignment resolution.
		// Bash bails out after NAMEREF_MAX hops with this warning and
		// then binds the value at the global scope.
		if prev.Kind == expand.NameRef && prev.Str == name && name != "" {
			r.errf("%swarning: %s: maximum nameref depth (8) exceeded\n",
				r.bashErrPrefix(r.curStmtPos), name)
			// Bash binds the value at the global scope. If that global
			// variable already exists as an array, a scalar assignment
			// lands in element [0] and keeps the array — so adopt the
			// global variable as the target to preserve its kind rather
			// than overwriting it with a plain scalar.
			if g := r.lookupGlobalVar(name); g.Kind == expand.Indexed || g.Kind == expand.Associative {
				prev = g
				preserveArrayKind = true
			}
		}
		if prev.Kind == expand.NameRef && as.Index == nil {
			if base, idx, ok := splitArrayRef(prev.Str); ok && syntax.ValidName(base) {
				// `local -n a='a[0]'; a=X` points the nameref at an
				// element of itself: resolving base `a` lands back on
				// the nameref. Bash reports this as circular and drops
				// the assignment rather than recursing.
				if baseVar := r.lookupVar(base); baseVar.Kind == expand.NameRef {
					if b2, _, ok2 := splitArrayRef(baseVar.Str); ok2 && b2 == base {
						prefix := r.bashErrPrefix(r.curStmtPos)
						if baseVar.Local {
							r.errf("%slocal: warning: %s: circular name reference\n", prefix, base)
						}
						r.errf("%swarning: %s: circular name reference\n", prefix, base)
						r.errf("%s`%s': not a valid identifier\n", prefix, prev.Str)
						return name, prev
					}
				}
				name = prev.Str
				prev = r.arrayElemAsScalar(idx, r.lookupVar(base))
			}
		}
		// A nameref chain that loops between distinct names (x→v→w→x)
		// can't be resolved to a real target. Bash warns and drops the
		// assignment, leaving the nameref itself untouched.
		origName := name
		origWasNameRef := prev.Kind == expand.NameRef
		if n, v, circular := prev.ResolveTracked(r.writeEnv); circular {
			r.errf("%swarning: %s: circular name reference\n",
				r.bashErrPrefix(r.curStmtPos), name)
			return name, prev
		} else if n != "" {
			name, prev = n, v
		}
		// `typeset -n a=b b; b=a[1]; a=foo` resolves a→b→a[1], so the
		// nameref a must become an indexed array of itself. Bash drops
		// the nameref attribute and warns before storing the element.
		if origWasNameRef {
			if base, _, ok := splitArrayRef(name); ok && base == origName {
				r.errf("%swarning: %s: removing nameref attribute\n",
					r.bashErrPrefix(r.curStmtPos), origName)
			}
		}
	}
	prevWasSet := prev.Set
	prev.Set = true
	if as.Value != nil {
		// Use the assignment-context literal expansion so `var=foo:~/bin`
		// expands the post-colon tilde to $HOME, matching bash.
		s, err := expand.LiteralForAssign(r.ecfg, as.Value)
		r.expandErr(err)
		if valType == "-n" && as.Index == nil && as.Array == nil {
			target := s
			if as.Append {
				target = prev.Str + target
			}
			if !validNameRefTarget(target) {
				prefix := r.bashErrPrefix(as.Value.Pos())
				if r.setVarFromBuiltin != "" {
					if prev.Local {
						r.errf("%s%s: `%s': invalid variable name for name reference\n", prefix, r.setVarFromBuiltin, target)
					} else {
						r.errf("%s%s: `%s': not a valid identifier\n", prefix, r.setVarFromBuiltin, target)
					}
				} else {
					r.errf("%s`%s': not a valid identifier\n", prefix, target)
				}
				r.exit.code = 1
				if r.setVarFromBuiltin != "" && !prev.Local {
					return name, expand.Variable{}
				}
				return name, prev
			}
		}
		// Integer attribute (declare -i): parse the RHS as an
		// arithmetic expression and evaluate it. For =, the result
		// replaces the value; for +=, it's added to the current
		// numeric value (also re-parsed as arithmetic — bash 5.3
		// honours integer-attribute math even when the prior value
		// was stored as a literal expression like `4+1`).
		if prev.Integer && valType != "-n" && valType != "-a" && valType != "-A" {
			arithEval := func(s string) (int, error) {
				if s == "" {
					return 0, nil
				}
				expr, perr := syntax.NewParser().Arithmetic(strings.NewReader(s))
				if perr != nil || expr == nil {
					return 0, perr
				}
				return expand.Arithm(r.ecfg, expr)
			}
			arithAssignErr := func(s string, err error) {
				if err == nil {
					return
				}
				if r.bashCompatErrors {
					err = bashIntegerAssignArithErr(s, err)
				}
				prefix := r.filename
				if prefix == "" {
					prefix = "bash"
				}
				r.expandErr(fmt.Errorf("%s: line %d: %s: %s",
					prefix, as.Value.Pos().Line(), s, err))
				// The caller's assignment-error path discards the command
				// (POSIX mode: exits), stopping later assignments in it;
				// Bash also drops the rest of the input line
				// (`x+=7 y=4; echo` prints nothing, arith9.sub).
				r.exit.code = 1
				if r.bashCompatErrors && !r.interactiveShell && !r.opts[optPosix] {
					r.discardRestOfLine = r.curStmtPos.Line()
				}
			}
			rhs, err := arithEval(s)
			if err != nil {
				arithAssignErr(s, err)
				return name, prev
			}
			if as.Append && prev.Kind == expand.Indexed {
				prev.Kind = expand.String
				prev.Str = strconv.Itoa(rhs)
				return name, prev
			}
			if as.Append {
				curStr := prev.Str
				// For indexed arrays the integer-flag bumps each
				// element through the same arithmetic-evaluate
				// path; pull the element's prior text from
				// prev.List so `a[i]+=N` reads N from there.
				// Bare `a+=N` (no index) on an integer indexed
				// array targets element 0 — same rule bash uses
				// for non-integer indexed arrays.
				switch {
				case as.Index != nil && prev.Kind == expand.Indexed:
					k := r.arithm(as.Index)
					if k < 0 {
						k = indexedNegativeOffset(prev, k)
					}
					if prev.IndexedSet(k) {
						curStr = prev.IndexedElem(k)
					} else {
						curStr = ""
					}
				case prev.Kind == expand.Indexed:
					if len(prev.List) > 0 {
						curStr = prev.IndexedElem(0)
					} else {
						curStr = ""
					}
				case as.Index != nil && prev.Kind == expand.Associative:
					if w, ok := as.Index.(*syntax.Word); ok {
						curStr = prev.Map[r.assocAssignKey(w)]
					}
				}
				cur, err := arithEval(curStr)
				if err != nil {
					arithAssignErr(curStr, err)
					return name, prev
				}
				rhs = cur + rhs
			}
			prev.Kind = expand.String
			prev.Str = strconv.Itoa(rhs)
			return name, prev
		}
		if !as.Append {
			// The array-kind-preserving logic only applies to
			// whole-variable assignments (`a=v`) in a declare-
			// family context (declare/readonly/local/export), not
			// to indexed forms or inline `v=foo cmd` calls.
			if as.Index == nil && (r.declAssignContext || preserveArrayKind) {
				// `declare -a name=(elem1 elem2 …)` reaching here
				// via string-form flattening hands us the raw
				// `(elem1 …)` text. Bash also applies this
				// compound-assignment parse when a plain `declare
				// name='(...)'` targets an existing array.
				if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
					arrayType := valType
					if arrayType == "" && !r.setVarStringParsed {
						switch prev.Kind {
						case expand.Indexed:
							arrayType = "-a"
						case expand.Associative:
							arrayType = "-A"
						}
					}
					if arrayType == "-a" || arrayType == "-A" {
						if file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("x="+s), ""); err == nil &&
							len(file.Stmts) == 1 {
							if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 1 && call.Assigns[0].Array != nil {
								return r.assignVal(name, prev, call.Assigns[0], arrayType)
							}
						}
						if arrayType == "-a" {
							prev.Kind = expand.Indexed
							prev.List = []string{}
						} else {
							prev.Kind = expand.Associative
							prev.Map = map[string]string{}
						}
						return name, prev
					}
				}
				if valType == "-a" {
					prev.Kind = expand.Indexed
					prev.List = []string{s}
					prev.ListSet = nil
					prev.Str = ""
					return name, prev
				}
				if valType == "-A" {
					prev.Kind = expand.Associative
					prev.Map = map[string]string{"0": s}
					prev.Str = ""
					return name, prev
				}
				// Bash: when the existing variable is an indexed or
				// associative array, a scalar assignment (`a=v`) in
				// declare-family context sets element [0] (or key
				// "0") rather than collapsing to a scalar.
				switch prev.Kind {
				case expand.Indexed:
					if valType != "-n" {
						newList := slices.Clone(prev.List)
						if len(newList) == 0 {
							newList = []string{s}
						} else {
							newList[0] = s
						}
						prev.List = newList
						if prev.ListSet != nil {
							prev.ListSet = prev.CloneListSet()
							prev.ListSet[0] = true
						}
						return name, prev
					}
				case expand.Associative:
					if valType != "-n" {
						newMap := make(map[string]string, len(prev.Map)+1)
						for k, v := range prev.Map {
							newMap[k] = v
						}
						newMap["0"] = s
						prev.Map = newMap
						prev.Set = true
						return name, prev
					}
				}
			}
			prev.Kind = expand.String
			if valType == "-n" {
				prev.Kind = expand.NameRef
			}
			prev.Str = s
			return name, prev
		}
		switch prev.Kind {
		case expand.String, expand.Unknown:
			// `declare -a s+=Y` on a scalar converts it to an indexed
			// array first; the append then targets element 0.
			if valType == "-a" && r.declAssignContext {
				prev.Kind = expand.Indexed
				prev.List = []string{prev.Str + s}
				prev.ListSet = nil
				prev.Str = ""
				return name, prev
			}
			prev.Kind = expand.String
			prev.Str += s
		case expand.NameRef:
			if valType == "-n" {
				prev.Str += s
				return name, prev
			}
		case expand.Indexed:
			// `arr[i]+=s` appends `s` onto the existing element
			// at index `i`. Leave subscript evaluation and current
			// value lookup to setVarWithIndex so side effects in the
			// subscript happen exactly once.
			if as.Index != nil {
				prev.Kind = expand.String
				prev.Str = s
				return name, prev
			}
			// Bare `arr+=s` (no index) targets element 0, per
			// bash's "treat as `arr[0]+=s`" rule.
			if len(prev.List) == 0 {
				prev.List = append(prev.List, "")
			}
			prev.List[0] += s
			if prev.ListSet != nil {
				prev.ListSet = prev.CloneListSet()
				prev.ListSet[0] = true
			}
		case expand.Associative:
			// `arr[k]+=s` for associative arrays appends `s`
			// onto the existing value at key `k`. setVarWithIndex
			// receives vr.Str and writes it to map[k] for us.
			if as.Index != nil {
				k, ok := r.assocAssignIndexKey(as.Index)
				if ok {
					prev.Kind = expand.String
					if prev.Integer {
						cur, _ := strconv.Atoi(r.integerArrayValue(prev.Map[k]))
						rhs, _ := strconv.Atoi(r.integerArrayValue(s))
						prev.Str = strconv.Itoa(cur + rhs)
					} else {
						prev.Str = prev.Map[k] + s
					}
					return name, prev
				}
				break
			}
			// `arr+=value` (no index) on an assoc array sets key
			// "0" — bash treats the bare-scalar append on an
			// assoc array as `arr[0]+=value`.
			newMap := maps.Clone(prev.Map)
			if newMap == nil {
				newMap = make(map[string]string)
			}
			if prev.Integer {
				cur, _ := strconv.Atoi(r.integerArrayValue(newMap["0"]))
				rhs, _ := strconv.Atoi(r.integerArrayValue(s))
				newMap["0"] = strconv.Itoa(cur + rhs)
			} else {
				newMap["0"] = newMap["0"] + s
			}
			prev.Map = newMap
			prev.Set = true
			return name, prev
		}
		return name, prev
	}
	if as.Array == nil {
		// don't return the zero value, as that's an unset variable
		if valType == "-n" {
			// `typeset -n NAME` (no value) converts an existing
			// scalar to a nameref pointing at whatever its
			// current value names (bash 5.3 behavior). Preserve
			// the existing Str — `foo=bar; typeset -n foo` keeps
			// foo's "bar" and dereferences it on read.
			prev.Kind = expand.NameRef
			return name, prev
		}
		// Plain `declare NAME` (no value, no -n): start fresh as
		// an empty string so `declare x; echo "<$x>"` shows `<>`.
		prev.Kind = expand.String
		prev.Str = ""
		return name, prev
	}
	// Array assignment.
	elems := as.Array.Elems
	if valType == "" {
		valType = "-a" // indexed
		switch {
		case as.Append && prev.Kind == expand.Associative:
			// `assoc+=([key]=value)` on an already-associative
			// var preserves the assoc kind even when the index
			// is a bare literal, not a quoted string. Plain
			// `assoc=(…)` (no `+=`) still falls through to the
			// inference below for back-compat.
			valType = "-A"
		case prev.Kind == expand.Associative:
			valType = "-A"
		}
	}
	if valType == "-A" {
		amap := make(map[string]string, len(elems))
		assocBuckets := prev.AssocBuckets
		// Converting an existing scalar mirrors bash's
		// convert_var_to_assoc: a 128-bucket hash table (which
		// `declare -p` key ordering reflects), with the old value
		// surviving at key "0" only for `+=`-style appends.
		if prev.Kind == expand.String && prev.Declared() {
			assocBuckets = 128
			if as.Append && prev.Set {
				amap["0"] = prev.Str
			}
		}
		// Inherit prev's map when this is a `+=`-style append at the
		// outer assignment level; per-element `[k]+=v` appends to the
		// previous value of that key.
		if as.Append && prev.Map != nil {
			for k, v := range prev.Map {
				amap[k] = v
			}
		}
		// bash 5.1+: a compound assignment to an associative array
		// whose first word is neither `[key]=value` nor assignment-
		// shaped is a list of alternating keys and values
		// (kvpair_assignment_p); a missing trailing value is empty.
		if len(elems) > 0 && elems[0].Index == nil && !wordLooksLikeAssign(elems[0].Value) {
			var words []string
			for _, elem := range elems {
				if elem.Index != nil {
					// A later [k]=v word in kvpair mode is plain
					// key/value text in bash; reassemble it.
					if w, ok := elem.Index.(*syntax.Word); ok && w != nil {
						words = append(words, "["+r.literal(w)+"]="+r.literalForAssign(elem.Value))
						continue
					}
				}
				words = append(words, r.literalForAssign(elem.Value))
			}
			for i := 0; i < len(words); i += 2 {
				if words[i] == "" {
					// An empty key is invalid for an associative array
					// (`foo=( "" null )` -> `[""]=null`); bash rejects it
					// and skips the pair.
					r.errf("%s\"\": bad array subscript\n", r.bashErrPrefix(r.curStmtPos))
					r.exit.code = 1
					continue
				}
				v := ""
				if i+1 < len(words) {
					v = words[i+1]
				}
				if prev.Integer {
					v = r.integerArrayValue(v)
				}
				amap[words[i]] = v
			}
			prev.Kind = expand.Associative
			prev.Map = amap
			prev.AssocBuckets = assocBuckets
			return name, prev
		}
		for _, elem := range elems {
			// `declare -A foo=([k]=v value)` — non-keyed element in an
			// associative-array context with a subscripted first word.
			// Bash 5.3 emits `<file>: line N: foo: <value>: must use
			// subscript when assigning associative array` and stops
			// processing the remaining elements.
			w, ok := elem.Index.(*syntax.Word)
			if !ok || w == nil {
				if elem.Value != nil {
					r.errf("%s%s: %s: must use subscript when assigning associative array\n",
						r.bashErrPrefix(r.curStmtPos), name, r.literalForAssign(elem.Value))
					r.exit.code = 1
				}
				break
			}
			k := r.assocAssignKey(w)
			v := r.literalForAssign(elem.Value)
			if elem.Append {
				if !as.Append {
					// Bash 5.3's compound associative assignment has a
					// residue bug: a [k]+=v element in a non-append
					// literal stores v, ignoring any earlier [k]=old in
					// the same literal. Outer a+=(...) still appends to
					// the existing map and earlier elements.
				} else if prev.Integer {
					cur, _ := strconv.Atoi(r.integerArrayValue(amap[k]))
					rhs, _ := strconv.Atoi(r.integerArrayValue(v))
					v = strconv.Itoa(cur + rhs)
				} else {
					v = amap[k] + v
				}
			} else if prev.Integer {
				v = r.integerArrayValue(v)
			}
			amap[k] = v
		}
		if !as.Append {
			prev.Kind = expand.Associative
			prev.Map = amap
			prev.AssocBuckets = assocBuckets
			return name, prev
		}
		prev.Map = amap
		prev.AssocBuckets = assocBuckets
		// `declare -A name+=(…)` in a declare-family context records
		// the associative kind so a previously-indexed variable trips
		// the cannot-convert check in setVar, like bash.
		if r.declAssignContext {
			prev.Kind = expand.Associative
		}
		return name, prev
	}
	// Expand all RHS values before modifying the array. Explicit subscript
	// arithmetic is evaluated later, while applying elements, because bash
	// lets a later subscript see earlier elements from the same compound
	// assignment (`a=([0]=1+2+3 [a[0]]=10)`). For non-append assignments we
	// expose that in-progress array only during subscript evaluation and
	// restore the previous binding immediately after, so declarations like
	// `local -a foo=([0]=v)` don't publish the value before assignVal returns.
	elemValues := make([]struct {
		indexExpr syntax.ArithmExpr
		values    []string
		append    bool // [idx]+=value
	}, len(elems))
	skipThrough := -1
	for i, elem := range elems {
		if i <= skipThrough {
			continue
		}
		if elem.Index != nil {
			if r.arrayExpandOnceIndexExprErr(elem.Index) {
				break
			}
			if idx, bad := literalBadIndexedAssignSubscript(elem.Index); bad {
				val := r.literalForAssign(elem.Value)
				if idx == "*" || idx == "@" {
					r.errf("%s[%s]=%s: cannot assign to non-numeric index\n",
						r.bashErrPrefix(r.curStmtPos), idx, val)
				} else {
					r.errf("%s[]=%s: bad array subscript\n",
						r.bashErrPrefix(r.curStmtPos), val)
				}
				r.exit.code = 1
				break
			}
			elemValues[i].indexExpr = elem.Index
			elemValues[i].values = []string{r.literalForAssign(elem.Value)}
			elemValues[i].append = elem.Append
		} else if elem.Value != nil && len(elem.Value.Parts) == 1 {
			// Bash keeps an unquoted bracket pattern inside a compound
			// array assignment as one word even when the pattern contains
			// blanks: a=( [ 123 ] ) has one element, "[ 123 ]".
			// The parser exposes the intervening words separately, so
			// reassemble the source span before pathname expansion.
			if first, ok := elem.Value.Parts[0].(*syntax.Lit); ok &&
				strings.HasPrefix(first.Value, "[") && !strings.Contains(first.Value, "]") {
				for j := i + 1; j < len(elems); j++ {
					next := elems[j]
					if next.Index != nil || next.Value == nil || len(next.Value.Parts) != 1 {
						break
					}
					lit, ok := next.Value.Parts[0].(*syntax.Lit)
					if !ok {
						break
					}
					if strings.Contains(lit.Value, "]") {
						pattern := r.sourceTextRange(elem.Value.Pos(), next.Value.End(), false)
						if pattern == "" {
							parts := make([]string, 0, j-i+1)
							for _, e := range elems[i : j+1] {
								parts = append(parts, e.Value.Lit())
							}
							pattern = strings.Join(parts, " ")
						}
						word := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: pattern}}}
						elemValues[i].values = r.fields(word)
						skipThrough = j
						break
					}
				}
			}
			if skipThrough < i {
				elemValues[i].values = r.fields(elem.Value)
			}
		} else {
			elemValues[i].values = r.fields(elem.Value)
		}
	}
	// Integer attribute on an array (`typeset -i arr; arr=(1+2 3+4)`)
	// evaluates each element value as an arithmetic expression. Apply
	// the same to `arr=([0]=7+11)` literals and to per-element `+=`
	// appends.
	if prev.Integer {
		arithEval := func(s string) string {
			if s == "" {
				return "0"
			}
			expr, perr := syntax.NewParser().Arithmetic(strings.NewReader(s))
			if perr != nil || expr == nil {
				return "0"
			}
			v, _ := expand.Arithm(r.ecfg, expr)
			return strconv.Itoa(v)
		}
		for i := range elemValues {
			for j, v := range elemValues[i].values {
				elemValues[i].values[j] = arithEval(v)
			}
		}
	}

	// `work` accumulates the elements via SetIndexed, which picks dense or
	// sparse (overlay) storage so a huge subscript does not balloon the slice.
	var index int
	work := prev
	work.Kind = expand.Indexed
	work.Str = ""
	work.List = nil
	work.ListSet = nil
	work.ListMap = nil
	if as.Append && prev.Kind == expand.Indexed {
		work.List = slices.Clone(prev.List)
		work.ListSet = prev.CloneListSet()
		work.ListMap = prev.CloneListMap()
		if idxs := prev.IndexedIndexes(); len(idxs) > 0 {
			index = idxs[len(idxs)-1] + 1
		}
	}
	publishWork := func() {
		work.Set = true
		if as.Append {
			_ = r.writeEnv.Set(name, work)
		}
	}
	withWorkVisible := func(fn func() bool) bool {
		if as.Append {
			publishWork()
			return fn()
		}
		if name == "" {
			return fn()
		}
		saved := r.writeEnv.Get(name)
		_ = r.writeEnv.Set(name, work)
		ok := fn()
		_ = r.writeEnv.Set(name, saved)
		return ok
	}
	for _, ev := range elemValues {
		if ev.indexExpr != nil {
			var ok bool
			if !withWorkVisible(func() bool {
				index, ok = r.arithmCompoundArrayIndex(ev.indexExpr)
				return ok
			}) {
				prev.Kind = expand.Indexed
				prev.List = []string{}
				prev.ListSet = nil
				prev.ListMap = nil
				prev.Str = ""
				return name, prev
			}
			if index < 0 {
				index = indexedNegativeOffset(work, index)
				if index < 0 {
					r.errf("%s[%s]=%s: bad array subscript\n",
						r.bashErrPrefix(r.curStmtPos), r.arithmSourceText(ev.indexExpr, false), strings.Join(ev.values, " "))
					r.exit.code = 1
					break
				}
			}
		}
		for i, str := range ev.values {
			elemIndex := index + i
			if ev.append && i == 0 {
				cur := work.IndexedElem(elemIndex)
				if prev.Integer {
					// Integer-attribute arrays: `[k]+=N` adds
					// arithmetically rather than concatenating.
					c, _ := strconv.Atoi(cur)
					rhs, _ := strconv.Atoi(str)
					str = strconv.Itoa(c + rhs)
				} else {
					str = cur + str
				}
			}
			work.SetIndexed(elemIndex, str)
		}
		index += len(ev.values)
	}
	if !as.Append {
		return name, work
	}
	switch prev.Kind {
	case expand.Unknown, expand.Indexed:
		// For Indexed, work was seeded with the prior elements above, so it
		// already replaces the previous array rather than concatenating.
		return name, work
	case expand.String:
		// String → Indexed: keep a set prior scalar at index 0 and
		// shift the new elements above it. A declared-but-unset
		// scalar (`declare a`) has no element to preserve.
		origScalar := prev.Str
		prev.Kind = expand.Indexed
		prev.Str = ""
		if prevWasSet {
			shifted := prev
			shifted.List = nil
			shifted.ListSet = nil
			shifted.ListMap = nil
			shifted.SetIndexed(0, origScalar)
			for _, i := range work.IndexedIndexes() {
				shifted.SetIndexed(i+1, work.IndexedElem(i))
			}
			prev.List = shifted.List
			prev.ListSet = shifted.ListSet
			prev.ListMap = shifted.ListMap
		} else {
			prev.List = work.List
			prev.ListSet = work.ListSet
			prev.ListMap = work.ListMap
		}
	case expand.Associative:
		// TODO
	default:
		// Should only happen if we forgot a case above.
		panic(fmt.Sprintf("unexpected conversion of kind %d", prev.Kind))
	}
	return name, prev
}

// bashCompatAtMost reports whether $BASH_COMPAT selects Bash level n or
// earlier. It takes "5.1" and "51" alike; unset or unparsable means the
// current level.
func (r *Runner) bashCompatAtMost(n int) bool {
	v := strings.ReplaceAll(strings.TrimSpace(r.envGet("BASH_COMPAT")), ".", "")
	level, err := strconv.Atoi(v)
	return err == nil && level > 0 && level <= n
}
