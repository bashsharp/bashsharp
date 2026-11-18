package shellexec

import (
	"context"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (sh *shell) taskPolicyContext(ctx context.Context) context.Context {
	if sh.cfg.lang == syntax.LangBashPP && shellrt.InTask(ctx) {
		return interp.WithTaskPolicy(ctx)
	}
	return ctx
}
