package interp

import "github.com/bashsharp/bashsharp/engine/syntax"

func (r *Runner) bashPPAgenticFunc(name string) bool {
	body := r.bashPPAgenticFuncs[name]
	return body != nil && body == r.Funcs[name]
}

func (r *Runner) bashPPAgenticCallError(pos syntax.Pos, name string) {
	r.errf("%s%s: agentic action requires an explicit agentic { ...; } scope\n", r.bashErrPrefix(pos), name)
	r.exit.code = 1
}
