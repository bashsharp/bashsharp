package interp

import "github.com/bashsharp/bashsharp/engine/syntax"

func (r *Runner) bashPPChannelOperation(word *syntax.Word, operation string) (*bashPPChannel, bool) {
	channel, ok := r.bashPPChannel(word)
	if !ok {
		return nil, false
	}
	name := r.literal(word)
	cell := r.bashPPScope.lookup(name).view()
	if typ, ok := cell.declType.(*syntax.BashPPChanType); ok {
		forbidden := typ.Direction == "send" && operation == "receive" || typ.Direction == "recv" && operation != "receive"
		if forbidden {
			r.errf("BASHPP-ECHAN-DIRECTION: cannot %s on %s channel %s\n", operation, typ.Direction+"-only", name)
			r.exit.code = 2
			if r.bashPPConcurrent != nil {
				r.bashPPConcurrent.cancel()
			}
			return nil, false
		}
	}
	return channel, true
}

func (r *Runner) bashPPCheckChannelArgs(fn *bashPPFunc, params []bashPPParam, channels []*bashPPChannel, cells []*bashPPCell) bool {
	for i, param := range params {
		required, ok := param.typ.(*syntax.BashPPChanType)
		if !ok {
			continue
		}
		if i < len(cells) && r.goSourceNativeChannelFits(cells[i], required) {
			continue
		}
		// An untyped nil argument acquires the parameter's channel type at
		// binding time. It has no channel capability to inspect, just as a bare
		// nil result has none before result-context coercion below.
		if r.bashPPGoSource && i < len(cells) && cells[i] != nil && cells[i].declType == nil && cells[i].interfaceValue != nil && cells[i].interfaceValue.nilIface {
			continue
		}
		var channel *bashPPChannel
		if i < len(channels) {
			channel = channels[i]
		}
		var actual *syntax.BashPPChanType
		if i < len(cells) && cells[i] != nil {
			actual, _ = cells[i].declType.(*syntax.BashPPChanType)
		}
		directionOK := actual == nil || actual.Direction == "" || actual.Direction == required.Direction
		element := ""
		if required.Element != nil {
			element = bashPPTypeText(required.Element)
		} else if required.Elem != nil {
			element = required.Elem.Value
		}
		if r.bashPPGoSource && channel == nil && actual != nil && directionOK && bashPPTypeText(actual.Element) == element && cells[i].vr.Str == "" {
			continue
		}
		if channel != nil && channel.elem == element && directionOK {
			continue
		}
		r.errf("BASHPP-EARG-CHAN: %s requires %s for parameter %s\n", fn.name(), bashPPTypeText(required), param.name)
		r.exit.code = 2
		if r.bashPPConcurrent != nil {
			r.bashPPConcurrent.cancel()
		}
		return false
	}
	return true
}

func (r *Runner) bashPPCheckChannelResult(fn *bashPPFunc, required syntax.BashPPTypeExpr, source *bashPPCell) bool {
	typ, ok := required.(*syntax.BashPPChanType)
	if !ok {
		return true
	}
	if r.goSourceNativeChannelFits(source, typ) {
		return true
	}
	// A bare nil return has not acquired its declared channel type yet. The
	// result-context coercion immediately after this check supplies that type;
	// accept the untyped nil cell here rather than rejecting it as a channel of
	// unknown direction and element.
	if r.bashPPGoSource && source != nil && source.declType == nil && source.interfaceValue != nil && source.interfaceValue.nilIface {
		return true
	}
	if r.bashPPGoSource && source != nil && source.channel == nil && source.vr.Str == "" {
		if actual, ok := source.declType.(*syntax.BashPPChanType); ok && bashPPTypeText(actual.Element) == bashPPTypeText(typ.Element) && (actual.Direction == "" || actual.Direction == typ.Direction) {
			return true
		}
	}
	if source != nil && source.channel != nil {
		actual, _ := source.declType.(*syntax.BashPPChanType)
		directionOK := actual == nil || actual.Direction == "" || actual.Direction == typ.Direction
		if directionOK && source.channel.elem == bashPPTypeText(typ.Element) {
			return true
		}
	}
	r.errf("BASHPP-ERETURN-CHAN: %s requires %s result\n", fn.name(), bashPPTypeText(typ))
	r.exit.code = 2
	r.exit.returning = false
	r.bashPPShortFailureSeq++
	if r.bashPPConcurrent != nil {
		r.bashPPConcurrent.cancel()
	}
	return false
}
