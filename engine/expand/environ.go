// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"cmp"
	"maps"
	"runtime"
	"slices"
	"strings"
)

// Environ is the base interface for a shell's environment, allowing it to fetch
// variables by name and to iterate over all the currently set variables.
type Environ interface {
	// Get retrieves a variable by its name. To check if the variable is
	// set, use Variable.IsSet.
	Get(name string) Variable

	// TODO(v4): make Each below a func that returns an iterator.

	// Each iterates over all the currently set variables, calling the
	// supplied function on each variable. Iteration is stopped if the
	// function returns false.
	//
	// The names used in the calls aren't required to be unique or sorted.
	// If a variable name appears twice, the latest occurrence takes
	// priority.
	//
	// Each is required to forward exported variables when executing
	// programs.
	Each(func(name string, vr Variable) bool)
}

// TODO(v4): [WriteEnviron.Set] below is overloaded to the point that correctly
// implementing both sides of the interface is tricky. In particular, some operations
// such as `export foo` or `readonly foo` alter the attributes but not the value,
// and `foo=bar` or `foo=[3]=baz` alter the value but not the attributes.

// WriteEnviron is an extension on Environ that supports modifying and deleting
// variables.
type WriteEnviron interface {
	Environ
	// Set sets a variable by name. If !vr.IsSet(), the variable is being
	// unset; otherwise, the variable is being replaced.
	//
	// The given variable can have the kind [KeepValue] to replace an existing
	// variable's attributes without changing its value at all.
	// This is helpful to implement `readonly foo=bar; export foo`,
	// as the second declaration needs to clearly signal that the value is not modified.
	//
	// An error may be returned if the operation is invalid, such as if the
	// name is empty or if we're trying to overwrite a read-only variable.
	Set(name string, vr Variable) error
}

type nameRefResolver interface {
	ResolveNameRef(name string) Variable
}

//go:generate go tool stringer -type=ValueKind

// ValueKind describes which kind of value the variable holds.
// While most unset variables will have an [Unknown] kind, an unset variable may
// have a kind associated too, such as via `declare -a foo` resulting in [Indexed].
type ValueKind uint8

const (
	// Unknown is used for unset variables which do not have a kind yet.
	Unknown ValueKind = iota
	// String describes plain string variables, such as `foo=bar`.
	String
	// NameRef describes variables which reference another by name, such as `declare -n foo=foo2`.
	NameRef
	// Indexed describes indexed array variables, such as `foo=(bar baz)`.
	Indexed
	// Associative describes associative array variables, such as `foo=([bar]=x [baz]=y)`.
	Associative

	// KeepValue is used by [WriteEnviron.Set] to signal that we are changing attributes
	// about a variable, such as exporting it, without changing its value at all.
	KeepValue

	// Object describes a variable holding a live Go value, kept in
	// [Variable.Obj]. It is an extension of the bash++ dialect
	// ([syntax.LangBashPP]) and is never produced when interpreting any other
	// language variant; see [Config.Lang].
	//
	// Objects exist so that a value can cross between the shell and Go code
	// without a lossy trip through a string. Wherever the shell needs a
	// string — a command argument, a `$foo` expansion, a comparison — the
	// value is coerced with [Variable.String], which renders it as JSON. That
	// same rendering is what an external binary receives, so a program which
	// only understands strings sees a plain JSON document and a Go caller
	// which understands more sees the original value.
	//
	// Note that Obj is not deep-copied when a Variable is copied, so an object
	// is shared, not snapshotted, by an assignment or a subshell.
	Object

	// Deprecated: use [Unknown], as tracking whether or not a variable is set
	// is now done via [Variable.Set].
	// Otherwise it was impossible to describe an unset variable with a known kind
	// such as `declare -A foo`.
	Unset = Unknown
)

// Variable describes a shell variable, which can have a number of attributes
// and a value.
type Variable struct {
	// Set is true when the variable has been set to a value,
	// which may be empty.
	Set bool

	Local    bool
	Exported bool
	ReadOnly bool
	// Integer is set when the variable was declared with `declare -i`.
	// Subsequent assignments evaluate the right-hand side as arithmetic
	// rather than treating it as a literal string.
	Integer bool

	// Upper / Lower / Capitalize are bash's `declare -u`, `-l`, and
	// `-c` attributes — they auto-fold assigned values to all-upper,
	// all-lower, or capitalize-first respectively. Mutually exclusive
	// in bash; setting one clears the others.
	Upper      bool
	Lower      bool
	Capitalize bool

	// Kind defines which of the value fields below should be used.
	Kind ValueKind

	Str     string            // Used when Kind is String or NameRef.
	Obj     any               // Used when Kind is Object; see [Object].
	List    []string          // Used when Kind is Indexed; holds the dense prefix [0,len(List)).
	ListMap map[int]string    // Used when Kind is Indexed; sparse overlay for indices >= maxDenseIndex.
	ListSet map[int]bool      // Used when Kind is Indexed and sparse; nil means every List index is set.
	Map     map[string]string // Used when Kind is Associative.

	// AssocBuckets records the bash hash-table bucket count that key
	// iteration order should be derived from when Kind is Associative.
	// Zero means the default 1024 buckets of a fresh `declare -A`;
	// bash converts an existing scalar to an associative array with a
	// 128-bucket table instead, which changes `declare -p` ordering.
	AssocBuckets uint16
}

// AssocKeysForDeclare returns the associative-array keys in the order
// bash's `declare -p` would print them, honoring the variable's hash
// bucket count (see [Variable.AssocBuckets]).
func (v Variable) AssocKeysForDeclare() []string {
	buckets := uint32(1024)
	if v.AssocBuckets != 0 {
		buckets = uint32(v.AssocBuckets)
	}
	// bash's hashlib.c hash_string: FNV-1 (multiply first, then XOR).
	hash := func(s string) uint32 {
		i := uint32(2166136261)
		for _, c := range []byte(s) {
			i = i * 16777619
			i = i ^ uint32(c)
		}
		return i
	}
	keys := make([]string, 0, len(v.Map))
	for k := range v.Map {
		keys = append(keys, k)
	}
	slices.SortStableFunc(keys, func(a, b string) int {
		ba := hash(a) % buckets
		bb := hash(b) % buckets
		if ba != bb {
			return int(ba) - int(bb)
		}
		return strings.Compare(a, b)
	})
	return keys
}

// IsSet reports whether the variable has been set to a value.
// The zero value of a Variable is unset.
func (v Variable) IsSet() bool {
	return v.Set
}

// Declared reports whether the variable has been declared.
// Declared variables may not be set; `export foo` is exported but not set to a value,
// and `declare -a foo` is an indexed array but not set to a value.
func (v Variable) Declared() bool {
	return v.Set || v.Local || v.Exported || v.ReadOnly || v.Integer ||
		v.Upper || v.Lower || v.Capitalize || v.Kind != Unknown
}

// Flags returns the variable's attribute flags in the order used by bash's
// declare builtin and ${var@a}: type (a/A/n), integer (i), readonly (r),
// exported (x), then case-modification (c/l/u). Bash 5.3 emits `-ai`, `-air`,
// `-rl`, `-arl`, etc. — type letter first, then `i`/`r`/`x`, then `c`/`l`/`u`.
func (v Variable) Flags() string {
	var flags []byte
	switch v.Kind {
	case Indexed:
		flags = append(flags, 'a')
	case Associative:
		flags = append(flags, 'A')
	case NameRef:
		if v.Integer {
			flags = append(flags, 'i')
		}
		flags = append(flags, 'n')
	}
	// Bash prints the integer/readonly/export attributes before the
	// case-modification ones, so e.g. `declare -lr x` reports `rl` and
	// `declare -alr arr` reports `arl` via ${x@a} / declare -p.
	if v.Integer && v.Kind != NameRef {
		flags = append(flags, 'i')
	}
	if v.ReadOnly {
		flags = append(flags, 'r')
	}
	if v.Exported {
		flags = append(flags, 'x')
	}
	if v.Capitalize {
		flags = append(flags, 'c')
	}
	if v.Lower {
		flags = append(flags, 'l')
	}
	if v.Upper {
		flags = append(flags, 'u')
	}
	return string(flags)
}

// String returns the variable's value as a string. In general, this only makes
// sense if the variable has a string value or no value at all.
func (v Variable) String() string {
	switch v.Kind {
	case String:
		return v.Str
	case Object:
		// An object's string coercion is its JSON encoding; this is what
		// `$foo` expands to and what an external binary is handed.
		return ObjectString(v.Obj)
	case Indexed:
		if v.IndexedSet(0) {
			return v.IndexedElem(0)
		}
	case Associative:
		// nothing to do
	}
	return ""
}

// maxDenseIndex bounds how far the dense List slice may grow. Indices at or
// above it are kept in the sparse ListMap overlay instead, so that a single
// huge subscript (bash stores indexed arrays sparsely, e.g. `a[0x7000004E]=x`)
// does not force a multi-gigabyte dense allocation. The invariant is that List
// only ever covers [0,maxDenseIndex) and ListMap only ever holds keys
// >= maxDenseIndex, so the two never overlap.
const maxDenseIndex = 1 << 20

// IndexedElem returns the value at array index i for an indexed array,
// consulting the sparse ListMap overlay for indices beyond the dense List
// prefix. It does not check whether the index is set; callers that care should
// guard with [Variable.IndexedSet].
func (v Variable) IndexedElem(i int) string {
	if i >= 0 && i < len(v.List) {
		return v.List[i]
	}
	if v.ListMap != nil {
		return v.ListMap[i]
	}
	return ""
}

// IndexedSet reports whether index is set for an indexed array variable.
// A nil ListSet preserves the historical dense-array representation: every
// in-range List entry is considered set, including empty-string elements.
func (v Variable) IndexedSet(index int) bool {
	if v.Kind != Indexed || index < 0 {
		return false
	}
	if v.ListSet == nil {
		return index < len(v.List)
	}
	return v.ListSet[index]
}

// IndexedIndexes returns the set indexes for an indexed array in ascending
// numeric order.
func (v Variable) IndexedIndexes() []int {
	if v.Kind != Indexed {
		return nil
	}
	if v.ListSet == nil {
		indexes := make([]int, len(v.List))
		for i := range indexes {
			indexes[i] = i
		}
		return indexes
	}
	indexes := make([]int, 0, len(v.ListSet))
	for i, ok := range v.ListSet {
		if ok && i >= 0 {
			indexes = append(indexes, i)
		}
	}
	slices.Sort(indexes)
	return indexes
}

// IndexedValues returns the set indexed-array values in ascending numeric
// index order.
func (v Variable) IndexedValues() []string {
	indexes := v.IndexedIndexes()
	values := make([]string, len(indexes))
	for i, index := range indexes {
		values[i] = v.IndexedElem(index)
	}
	return values
}

// SetIndexed assigns value to array index i, choosing dense slice storage for
// small indices and the sparse ListMap overlay for huge ones so that a large
// index does not force a multi-gigabyte dense allocation. It maintains ListSet
// and preserves the historical "nil ListSet means fully dense" fast path while
// the write merely extends a contiguous array. The receiver's List, ListSet
// and ListMap must already be owned (cloned) by the caller when shared.
func (v *Variable) SetIndexed(i int, value string) {
	if i < 0 {
		return
	}
	if i < maxDenseIndex {
		// A contiguous extend or in-range overwrite keeps the array dense,
		// so the nil-ListSet fast path can be preserved.
		if v.ListSet == nil && i <= len(v.List) {
			if i == len(v.List) {
				v.List = append(v.List, value)
			} else {
				v.List[i] = value
			}
			return
		}
		// A gap or an already-sparse array needs an explicit set map.
		if v.ListSet == nil {
			v.ListSet = v.DenseListSet()
		}
		if i >= len(v.List) {
			v.List = append(v.List, make([]string, i-len(v.List)+1)...)
		}
		v.List[i] = value
		v.ListSet[i] = true
		return
	}
	// Huge index: store in the sparse overlay instead of padding List.
	if v.ListSet == nil {
		v.ListSet = v.DenseListSet()
	}
	if v.ListMap == nil {
		v.ListMap = make(map[int]string)
	}
	v.ListMap[i] = value
	v.ListSet[i] = true
}

// IndexedCount returns the number of set elements in an indexed array.
func (v Variable) IndexedCount() int {
	return len(v.IndexedIndexes())
}

// CloneListSet returns a copy of the sparse indexed-array set map.
func (v Variable) CloneListSet() map[int]bool {
	if v.ListSet == nil {
		return nil
	}
	clone := make(map[int]bool, len(v.ListSet))
	for i, ok := range v.ListSet {
		if ok {
			clone[i] = true
		}
	}
	return clone
}

// CloneListMap returns a copy of the sparse indexed-array overlay map.
func (v Variable) CloneListMap() map[int]string {
	if v.ListMap == nil {
		return nil
	}
	return maps.Clone(v.ListMap)
}

// DenseListSet returns a sparse set map with every current List index marked
// as set. It is useful before deleting from a historically dense array.
func (v Variable) DenseListSet() map[int]bool {
	if v.Kind != Indexed {
		return nil
	}
	if v.ListSet != nil {
		return v.CloneListSet()
	}
	set := make(map[int]bool, len(v.List))
	for i := range v.List {
		set[i] = true
	}
	return set
}

// maxNameRefDepth defines the maximum number of times to follow references when
// resolving a variable. Otherwise, simple name reference loops could crash a
// program quite easily.
const maxNameRefDepth = 100

// Resolve follows a number of nameref variables, returning the last reference
// name that was followed and the variable that it points to.
func (v Variable) Resolve(env Environ) (string, Variable) {
	name, vr, _ := v.ResolveTracked(env)
	return name, vr
}

// ResolveTracked is like [Variable.Resolve] but also reports whether the
// nameref chain looped back on itself (a circular reference between
// distinct names, e.g. x→v→w→x). Such a chain resolves to an unset
// variable; bash additionally emits a "circular name reference" warning.
func (v Variable) ResolveTracked(env Environ) (string, Variable, bool) {
	name := ""
	var seen map[string]bool
	for range maxNameRefDepth {
		if v.Kind != NameRef {
			return name, v, false
		}
		name = v.Str // keep name for the next iteration
		if name != "" {
			if seen[name] {
				return name, Variable{}, true
			}
			if seen == nil {
				seen = make(map[string]bool)
			}
			seen[name] = true
		}
		if resolver, ok := env.(nameRefResolver); ok {
			v = resolver.ResolveNameRef(name)
		} else {
			v = env.Get(name)
		}
	}
	return name, Variable{}, true
}

// FuncEnviron wraps a function mapping variable names to their string values,
// and implements [Environ]. Empty strings returned by the function will be
// treated as unset variables. All variables will be exported.
//
// Note that the returned Environ's Each method will be a no-op.
func FuncEnviron(fn func(string) string) Environ {
	return funcEnviron(fn)
}

type funcEnviron func(string) string

func (f funcEnviron) Get(name string) Variable {
	value := f(name)
	if value == "" {
		return Variable{}
	}
	return Variable{Set: true, Exported: true, Kind: String, Str: value}
}

func (f funcEnviron) Each(func(name string, vr Variable) bool) {}

// ListEnviron returns an [Environ] with the supplied variables, in the form
// "key=value". All variables will be exported. The last value in pairs is used
// if multiple values are present.
//
// On Windows, where environment variable names are case-insensitive, the
// resulting variable names will all be uppercase.
func ListEnviron(pairs ...string) Environ {
	return listEnviron_(runtime.GOOS == "windows", pairs...)
}

// listEnviron_ implements [ListEnviron], but letting the tests specify
// whether to uppercase all names or not.
func listEnviron_(caseInsensitive bool, pairs ...string) Environ {
	list := slices.Clone(pairs)
	env := listEnviron{caseInsensitive: caseInsensitive}
	slices.SortStableFunc(list, func(a, b string) int {
		isep := strings.IndexByte(a, '=')
		jsep := strings.IndexByte(b, '=')
		if isep < 0 {
			isep = 0
		} else {
			isep += 1
		}
		if jsep < 0 {
			jsep = 0
		} else {
			jsep += 1
		}
		return env.compare(a[:isep], b[:jsep])
	})

	last := ""
	for i := 0; i < len(list); {
		name, _, ok := strings.Cut(list[i], "=")
		if name == "" || !ok {
			// invalid element; remove it
			list = slices.Delete(list, i, i+1)
			continue
		}
		if env.compare(last, name) == 0 {
			// duplicate; the last one wins
			list = slices.Delete(list, i-1, i)
			continue
		}
		last = name
		i++
	}
	env.pairs = list
	return env
}

// listEnviron is a sorted list of "name=value" strings.
type listEnviron struct {
	caseInsensitive bool
	pairs           []string
}

func (l listEnviron) compare(a, b string) int {
	if l.caseInsensitive {
		// This is not particularly efficient, but it does the job.
		// If we had a cmp-compatible version of [strings.EqualFold], we'd use it.
		a = strings.ToUpper(a)
		b = strings.ToUpper(b)
	}
	return strings.Compare(a, b)
}

func (l listEnviron) Get(name string) Variable {
	eqpos := len(name)
	endpos := len(name) + 1
	i, ok := slices.BinarySearchFunc(l.pairs, name, func(pair, name string) int {
		if len(pair) < endpos {
			// Too short; see if we are before or after the name.
			return l.compare(pair, name)
		}
		// Compare the name prefix, then the equal character.
		c := l.compare(pair[:eqpos], name)
		eq := pair[eqpos]
		if c == 0 {
			return cmp.Compare(eq, '=')
		}
		return c
	})
	if ok {
		return Variable{Set: true, Exported: true, Kind: String, Str: l.pairs[i][endpos:]}
	}
	return Variable{}
}

func (l listEnviron) Each(fn func(name string, vr Variable) bool) {
	for _, pair := range l.pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			// should never happen; see listEnvironWithUpper
			panic("expand.listEnviron: did not expect malformed name-value pair: " + pair)
		}
		if !fn(name, Variable{Set: true, Exported: true, Kind: String, Str: value}) {
			return
		}
	}
}
