package lower

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
)

// A failed owner receive leaves its declared value absent while the owner
// finishes its current source sequence. Canceled task frames unwind instead.
func (e *emitter) receiveDeclaration(n *syntax.BashPPShortDecl) (string, error) {
	if len(n.Lhs) < 1 || len(n.Lhs) > 2 {
		return "", e.fail(n, CodeResult, "receive declaration needs one or two bindings")
	}
	c := e.runtimeScope()
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	ch, err := e.valueWord(n.Recv.Chan)
	if err != nil {
		return "", err
	}
	ns := names(n.Lhs)
	failure := fmt.Sprintf("%sreceiveError%d_%d", e.prefix, n.Pos().Line(), n.Pos().Col())
	okName := failure + "OK"
	if len(ns) == 2 {
		okName = ns[1]
	}
	for i, name := range ns {
		info := scalarProjection()
		if i == 1 {
			info.sourceType = "bool"
		}
		info.present = failure + " == nil"
		if i == 0 {
			info.emptyWhen = failure + " == nil && !" + okName
		}
		e.bind(name)
		e.projections.projectionBind(name, info)
	}
	rt := e.prefix + "rt."
	receive := rt + "Receive(" + strings.Join([]string{c.Context, c.Session, c.Channels, ch}, ",") + ")"
	if info, ok := e.projections.projectionLookup(n.Recv.Chan.Lit()); ok && info.capabilityElement != "" {
		receive = rt + "ReceiveCapability[" + info.capabilityElement + "](" + c.Context + "," + e.program() + ".ResultSidecars,&" + ch + ")"
	}
	text := ns[0] + ", " + okName + ", " + failure + " := " + receive + "\n"
	text += "if " + failure + " != nil {\n" + e.program() + ".ReceiveFailure(" + failure + ")\nif " + c.Context + ".Err() != nil { panic(" + rt + "ChannelAbort{Err:" + failure + "}) }\n}"
	return text + e.unused(append(ns, okName)), nil
}
