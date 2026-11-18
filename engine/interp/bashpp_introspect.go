// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"go/constant"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Session value introspection (Sprint 221, B27b): the engine half of
// `bashy define <var>` answering with a live Bash# value's Go-spelled type
// and fields, the PowerShell Get-Member shape. It is a bounded READ of what
// the session already holds — one plain name in, one description out — and
// deliberately not an evaluator or debugger: selectors, indexes and
// expressions are refused rather than evaluated, nothing is invoked or
// converted, and no cell is mutated or dereference-chased. A handle — a
// pointer, channel or function value — reports its type and nil-ness with the
// value withheld; a record's private (lowercase) fields and opaque field
// values are listed by name and type with the value withheld the same way,
// and nothing reveals them.

// ValueField is one declared field of a described record value, in
// declaration order.
type ValueField struct {
	// Name and Type are the field's declared name and Go-spelled type.
	Name string
	Type string
	// Value is the field's rendered value, present only for an exported
	// field holding a plain scalar. Composite fields render their shape
	// through Type alone.
	Value string
	// Redacted marks a field whose value is withheld: a private (lowercase)
	// field, or an opaque value (channel, function, pointer) whose only
	// printable form would be an internal identity.
	Redacted bool
}

// ValueDescription is a read-only report of one session value, the answer
// [Runner.DescribeValue] gives the `define` surface.
type ValueDescription struct {
	// Name is the variable's name as resolved.
	Name string
	// Type is the value's Go-spelled type: the declared type of a typed
	// Bash# cell (the dynamic type for a non-nil interface value), or the
	// shell shape of a classic variable ("string", "[]string",
	// "map[string]string").
	Type string
	// Kind classifies the shape: "scalar", "record", "list", "map",
	// "handle" (pointer, channel, function value), or "nil" (a nil
	// interface value).
	Kind string
	// Value is the rendered value of a scalar. Composites and handles leave
	// it empty; handles set Redacted instead.
	Value string
	// Redacted reports that the value itself is withheld because it is
	// opaque — its only printable form would be an internal identity.
	Redacted bool
	// Nil reports a nil handle or nil interface value.
	Nil bool
	// Len is the element count of a list or map.
	Len int
	// Fields are a record's declared fields, in declaration order.
	Fields []ValueField
}

// DescribeValue reports the named session value, read-only. The name must be
// a plain variable name: anything else — a selector, an index, an expression
// — reports false rather than being evaluated. A Bash# typed value answers
// with its declared type and shape; any other set shell variable answers
// through its shell shape; an unset name reports false.
func (r *Runner) DescribeValue(name string) (ValueDescription, bool) {
	if !syntax.ValidName(name) {
		return ValueDescription{}, false
	}
	if r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(name); cell != nil {
			d := r.bashPPDescribeCell(cell)
			d.Name = name
			return d, true
		}
	}
	vr := r.LiveVar(name)
	if !vr.IsSet() {
		return ValueDescription{}, false
	}
	d := ValueDescription{Name: name}
	switch vr.Kind {
	case expand.Indexed:
		d.Type, d.Kind, d.Len = "[]string", "list", len(vr.List)
	case expand.Associative:
		d.Type, d.Kind, d.Len = "map[string]string", "map", len(vr.Map)
	default:
		d.Type, d.Kind, d.Value = "string", "scalar", vr.String()
	}
	return d, true
}

// DescribeValue answers for an in-process tool mid-run — the `define`
// command reaching the session it runs in — with exactly what
// [Runner.DescribeValue] reports between runs.
func (hc HandlerContext) DescribeValue(name string) (ValueDescription, bool) {
	if hc.runner == nil {
		return ValueDescription{}, false
	}
	return hc.runner.DescribeValue(name)
}

// bashPPDescribeCell classifies one typed cell without changing it.
func (r *Runner) bashPPDescribeCell(cell *bashPPCell) ValueDescription {
	// A description is a reading of the whole cell, so a shared one is read
	// from a snapshot; see bashpp_cell_share.go.
	cell = cell.view()
	if iface := cell.interfaceValue; iface != nil {
		if iface.nilIface || iface.cell == nil {
			return ValueDescription{Type: r.bashPPDescribeType(cell), Kind: "nil", Nil: true}
		}
		// Get-Member describes the runtime object: a non-nil interface value
		// answers with its dynamic type and shape.
		return r.bashPPDescribeCell(iface.cell)
	}
	typeText := r.bashPPDescribeType(cell)
	if cell.pointer || cell.pointerValue != nil {
		nil_ := cell.nilPointer || cell.pointerValue == nil
		return ValueDescription{Type: typeText, Kind: "handle", Redacted: !nil_, Nil: nil_}
	}
	declared := cell.declType
	if declared == nil {
		if meta := bashPPCellMeta(cell); meta != nil {
			declared = meta.typ
		}
	}
	var underlying syntax.BashPPTypeExpr
	if declared != nil {
		underlying = r.bashPPUnderlyingType(declared)
	}
	_, chanDeclared := underlying.(*syntax.BashPPChanType)
	if chanDeclared || cell.channel != nil || strings.HasPrefix(cell.vr.Str, bashPPChanHandlePrefix) {
		if !chanDeclared {
			typeText = "chan"
			if cell.channel != nil && cell.channel.elem != "" {
				typeText = "chan " + cell.channel.elem
			}
		}
		held := cell.channel != nil || cell.vr.Str != ""
		return ValueDescription{Type: typeText, Kind: "handle", Redacted: held, Nil: !held}
	}
	if _, ok := underlying.(*syntax.BashPPFuncType); ok || strings.HasPrefix(cell.vr.Str, bashPPFuncHandlePrefix) {
		if underlying == nil {
			typeText = "func"
		}
		held := cell.vr.String() != ""
		return ValueDescription{Type: typeText, Kind: "handle", Redacted: held, Nil: !held}
	}
	meta := bashPPCellMeta(cell)
	kind := ""
	if meta != nil {
		kind = meta.kind
	}
	switch {
	case kind == "struct" || bashPPStructUnderlying(underlying) != nil:
		d := ValueDescription{Type: typeText, Kind: "record"}
		d.Fields = r.bashPPDescribeFields(cell, underlying)
		return d
	case kind == "slice" || kind == "array" || kind == "inferred-array":
		d := ValueDescription{Type: typeText, Kind: "list"}
		if seq, ok := cell.vr.Obj.([]any); ok {
			d.Len = len(seq)
		} else if cell.vr.Kind == expand.Indexed {
			d.Len = len(cell.vr.List)
		}
		return d
	case kind == "map":
		d := ValueDescription{Type: typeText, Kind: "map"}
		if m, ok := cell.vr.Obj.(map[string]any); ok {
			d.Len = len(m)
		}
		return d
	case cell.vr.Kind == expand.Indexed:
		return ValueDescription{Type: typeText, Kind: "list", Len: len(cell.vr.List)}
	case cell.vr.Kind == expand.Associative:
		return ValueDescription{Type: typeText, Kind: "map", Len: len(cell.vr.Map)}
	}
	return ValueDescription{Type: typeText, Kind: "scalar", Value: cell.vr.String()}
}

// bashPPDescribeType spells a cell's type without inventing one: the declared
// type when present, the payload's own type next, the named type, the scalar
// kind an untyped declaration retained, and the scalar's shell shape as the
// last resort.
func (r *Runner) bashPPDescribeType(cell *bashPPCell) string {
	if cell.declType != nil {
		return bashPPTypeText(cell.declType)
	}
	if meta := bashPPCellMeta(cell); meta != nil && meta.typ != nil {
		return bashPPTypeText(meta.typ)
	}
	if cell.typeName != "" {
		return cell.typeName
	}
	switch cell.scalarKind {
	case constant.Int:
		return "int"
	case constant.Float:
		return "float64"
	case constant.Bool:
		return "bool"
	}
	return "string"
}

// bashPPStructUnderlying reports a struct underlying type, resolved or
// spelled inline.
func bashPPStructUnderlying(typ syntax.BashPPTypeExpr) *syntax.BashPPStructType {
	st, _ := typ.(*syntax.BashPPStructType)
	return st
}

// bashPPDescribeFields lists a record's declared fields in order. Values are
// read from the cell's storage without conversion; a private field or an
// opaque value is listed with its value withheld.
func (r *Runner) bashPPDescribeFields(cell *bashPPCell, underlying syntax.BashPPTypeExpr) []ValueField {
	st := bashPPStructUnderlying(underlying)
	if st == nil {
		return nil
	}
	obj, _ := cell.vr.Obj.(map[string]any)
	meta := bashPPCellMeta(cell)
	var fields []ValueField
	for _, field := range st.Fields {
		typeText := ""
		if field.FieldTypeExpr != nil {
			typeText = bashPPTypeText(field.FieldTypeExpr)
		} else if field.FieldType != nil {
			typeText = field.FieldType.Value
		}
		for _, name := range field.Names {
			f := ValueField{Name: name.Value, Type: typeText}
			if !bashPPExportedName(name.Value) {
				f.Redacted = true
				fields = append(fields, f)
				continue
			}
			var child *bashPPCollectionMeta
			if meta != nil {
				child = meta.mapping[name.Value]
			}
			value, ok := bashPPStorageGet(obj, name.Value)
			switch {
			case bashPPOpaqueField(r.bashPPUnderlyingType(field.FieldTypeExpr), child):
				f.Redacted = true
			case !ok || value == nil:
			case bashPPScalarStorage(value):
				f.Value = fmt.Sprint(value)
			}
			fields = append(fields, f)
		}
	}
	return fields
}

// bashPPExportedName follows Go's rule: a field is public when its first rune
// is upper case.
func bashPPExportedName(name string) bool {
	first, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(first)
}

// bashPPOpaqueField reports a field whose value's only printable form would
// be an internal identity: a channel, a function value, or a pointer.
func bashPPOpaqueField(typ syntax.BashPPTypeExpr, meta *bashPPCollectionMeta) bool {
	if meta != nil && meta.channel != nil {
		return true
	}
	switch typ.(type) {
	case *syntax.BashPPChanType, *syntax.BashPPFuncType, *syntax.BashPPPointerType:
		return true
	}
	return false
}

// bashPPScalarStorage reports storage a field can render directly: the plain
// scalars, not a nested composite.
func bashPPScalarStorage(value any) bool {
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	}
	return false
}
