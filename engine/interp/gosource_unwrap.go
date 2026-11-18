package interp

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Only the single-error protocol is admitted. Returning []error needs a
// separate alias/lifetime contract for the traversal's live slice storage.
func (l *bashPPLocalTypeSet) mirroredUnwrap(decls []*syntax.BashPPFuncDecl) []bashPPLocalMethod {
	if l.declared["error"] != nil {
		return nil
	}
	for _, d := range decls {
		if d.Name == nil || d.Name.Value != "Unwrap" || d.Receiver == nil || len(d.TypeParams) > 0 || len(d.Receiver.TypeParams) > 0 || len(d.Params) > 0 || len(d.Results) != 1 || len(d.Results[0].Names) > 1 {
			continue
		}
		result, ok := l.source(d.Results[0].FieldTypeExpr, 0)
		if !ok || result != "error" {
			continue
		}
		return []bashPPLocalMethod{{Name: "Unwrap", Pointer: d.Receiver.Pointer, Results: []string{"error"}}}
	}
	return nil
}

func synchronousUnwrapCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "errors" {
		return false
	}
	// Existing validation has already refused omitted custom Is methods and
	// unsupported references. The caller also refuses first-class function
	// callbacks before admitting this reviewed method-only operation.
	return name == "Unwrap" && len(q.Args) == 1 || name == "Is" && len(q.Args) == 2
}
