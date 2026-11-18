package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A MATERIALISED ORIGINAL TYPE'S METHODS BELONG TO THE INTERPRETER.
//
// The helper materialises the original program's named types as real Go
// declarations so reflect sees the original field names and element types
// (bashpp_native_local_types.go), but it never holds an original method body:
// only the reviewed protocol method sets (String/Error, Read, image.Image) are
// mirrored as stubs. A value of such a type that reaches the original program
// back through reflect — reflect.Value.Interface() of a pointer the walk
// selected — therefore crosses as a helper handle whose dynamic type declares
// none of the methods the program wrote. Dispatching one of those on that
// handle asks the helper for a method it does not have.
//
// The interpreter does not need the helper for any of it. Every pointer the
// transport hands over is registered under a session ORIGIN, the identity the
// cyclic-pointee back-reference already relies on
// (bashpp_sprint165_runtime2_cycle.go), and the helper's decoder keeps that
// origin for the pointer it decoded, so a handle minted anywhere in the
// materialised graph names the interpreter's own storage
// ([encodeHandle] in bashpp_native_worker.go.txt stamps it). Recovering the
// original pointer from the origin returns the value to the interpreter whole:
// its fields are read, its methods run and its pointer identity compares
// exactly as they do for the variable the program still holds — which is what
// keeps a walk's seen-pointer map (`map[Node]int`) answering.
//
// Nothing is normalised. Adoption requires the origin to be this session's,
// to resolve to a registered pointer, and that pointer's own cell to have the
// very named type the handle reports; a handle failing any of those stays the
// helper's, exactly as before.

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceAdoptOriginalHandles gives every reply handle that names a
// materialised original type the interpreter's own value for it.
func (r *Runner) goSourceAdoptOriginalHandles(req bashPPEvalRequest, values []bashPPBridgeValue) {
	if !r.bashPPGoSource || req.Bridge == nil {
		return
	}
	for i := range values {
		if cell, ok := r.goSourceOriginalHandleCell(req, values[i]); ok {
			values[i].localCell = cell
		}
	}
}

// goSourceOriginalHandleCell is the interpreter cell a handle of a
// materialised original type stands for, or false for any other handle.
func (r *Runner) goSourceOriginalHandleCell(req bashPPEvalRequest, v bashPPBridgeValue) (*bashPPCell, bool) {
	session := req.Bridge
	if v.Kind != "handle" || v.Function || v.localCell != nil || v.localReflect != nil {
		return nil, false
	}
	if v.Origin == 0 || v.Session != session.id {
		return nil, false
	}
	// Only a pointer to a materialised original named type: that is the whole
	// shape whose method set the helper cannot serve and whose identity the
	// origin names.
	element, pointer := strings.CutPrefix(v.Type, "*")
	if !pointer {
		return nil, false
	}
	typeName := bashPPLocalTypeName(element)
	if typeName == "" || strings.ContainsAny(typeName, "*[]") {
		return nil, false
	}
	materialised := false
	for _, local := range req.LocalTypes {
		if local.Name == typeName && !local.Alias {
			materialised = true
			break
		}
	}
	if !materialised {
		return nil, false
	}
	ptr, _ := session.originLookup(v.Origin)
	if ptr == nil || ptr.target == nil {
		return nil, false
	}
	cell := bashPPPointerCell(ptr)
	if cell == nil || cell.typeName != typeName {
		return nil, false
	}
	if v.Interface == "" {
		return cell, true
	}
	// The handle crossed as the dynamic value of an interface — the `any`
	// reflect.Value.Interface() answers. The interface box is the static type
	// the program asserts on; the pointer is its dynamic value.
	boxed := &bashPPCell{
		vr:       expand.Variable{Set: true, Kind: expand.String},
		declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: v.Interface}},
	}
	boxed.interfaceValue = &bashPPInterfaceValue{cell: cell, dynamic: cell.declType}
	return boxed, true
}
