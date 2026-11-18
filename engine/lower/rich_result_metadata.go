package lower

import (
	"fmt"
	"strconv"
	"strings"
)

// Rich results are the compile-side half of shellrt's result descriptor. A
// declared result type is source-defined and stays exactly as written: the
// motivating source declares `type Ch int` and returns a live channel in a `Ch`
// result, and the artifact's `Ch` remains an `int`. What changes is where the
// channel authority goes — into an explicit sidecar keyed by the destination
// cell's storage identity — not what the signature says.
//
// This file plans that split and renders the runtime calls. It performs no
// inference of its own: the payload spelling is whatever the caller already
// determined for the returned expression, so classification stays a bounded,
// reviewable decision rather than a second type system.
//
// The result arity is the source signature's; neither this file nor the
// runtime imposes a cap of its own.
//
// Nothing here is reachable from the emitter yet. The dispatcher, the return
// statement, the tuple assignment, the receive lowering, the child region's
// sidecar fork and the public source-signature wrappers in
// callables_runtime.go are all owned elsewhere; the hooks they need are listed
// in the accompanying commit message. Until they are wired, the exact `5:6:7`
// source is still rejected by Compile.

// richResultClass is how one declared result reaches its destination.
type richResultClass int

const (
	// richDirect is an ordinary native result: the declared storage holds the
	// whole value, and the existing conversion path is correct for it.
	richDirect richResultClass = iota
	// richCapability is a result whose native payload is an authority the
	// declared type cannot hold — today, exclusively a channel. The declared
	// storage keeps the callee's declared value and the authority travels in a
	// sidecar bound to the destination's identity.
	richCapability
	// richUnrepresentable is a payload this runtime carries neither as a
	// value nor as a capability. It is an explicit rejection: a result is
	// never zero-filled or dropped to make a signature fit.
	richUnrepresentable
)

func (c richResultClass) String() string {
	switch c {
	case richDirect:
		return "direct"
	case richCapability:
		return "capability"
	case richUnrepresentable:
		return "unrepresentable"
	}
	return "unknown"
}

// richResultDescriptor is one declared result of one callable. Declared is the
// source result type as the emitter spells it; Payload is the native type of
// the expression being returned into it, as already determined by the caller,
// and is empty when that is not known. Name is the named result cell, if any.
type richResultDescriptor struct {
	Index                   int
	Name, Declared, Payload string
}

// richResultPlan is the per-callable decision. Classes is parallel to Results.
type richResultPlan struct {
	Results []richResultDescriptor
	Classes []richResultClass
}

// NeedsFrame reports whether any result needs the descriptor at all. A
// callable whose results are all direct keeps the existing lowering untouched,
// which is what makes this addition safe to wire incrementally.
func (p richResultPlan) NeedsFrame() bool {
	for _, class := range p.Classes {
		if class != richDirect {
			return true
		}
	}
	return false
}

// Capabilities lists the results that carry an authority sidecar.
func (p richResultPlan) Capabilities() []richResultDescriptor {
	var out []richResultDescriptor
	for i, class := range p.Classes {
		if class == richCapability {
			out = append(out, p.Results[i])
		}
	}
	return out
}

// richResultPlanFor classifies every declared result. An unrepresentable
// result is recorded as such and reported where it would escape, so a
// diagnostic names the boundary the source actually crossed.
func (e *emitter) richResultPlanFor(results []richResultDescriptor) richResultPlan {
	plan := richResultPlan{Results: results, Classes: make([]richResultClass, len(results))}
	for i, descriptor := range results {
		plan.Classes[i] = e.classifyRichResult(descriptor)
	}
	return plan
}

// classifyRichResult decides one result. The rules are deliberately few:
//
//   - an unknown payload is direct, which is exactly today's behaviour, so
//     wiring this file cannot regress a source that compiles now;
//   - a payload that spells the declared type, or its underlying type, is
//     direct;
//   - a channel payload in a non-channel declared type is a capability;
//   - a function payload in a non-function declared type is unrepresentable,
//     because this runtime carries channel capabilities only;
//   - anything else is direct, and the ordinary Go build remains the authority
//     on whether the conversion is legal.
func (e *emitter) classifyRichResult(d richResultDescriptor) richResultClass {
	declared, payload := strings.TrimSpace(d.Declared), strings.TrimSpace(d.Payload)
	if declared == "" || payload == "" {
		return richDirect
	}
	underlying := e.richUnderlyingType(declared)
	if payload == declared || payload == underlying {
		return richDirect
	}
	switch {
	case richChannelType(payload):
		if richChannelType(underlying) {
			return richDirect
		}
		return richCapability
	case richFunctionType(payload):
		if richFunctionType(underlying) {
			return richDirect
		}
		return richUnrepresentable
	}
	return richDirect
}

// richUnderlyingType resolves a source-declared name to the type it was
// declared as, so `Ch` is compared as the `int` it actually is. Names that are
// not source declarations resolve to themselves.
func (e *emitter) richUnderlyingType(name string) string {
	seen := map[string]bool{}
	for !seen[name] {
		seen[name] = true
		declaration := e.declaredTypes[name]
		if declaration == nil {
			return name
		}
		switch {
		case declaration.DeclType != nil:
			name = declaration.DeclType.Value
		case declaration.DeclTypeExpr != nil:
			text, err := e.typeExpr(declaration.DeclTypeExpr)
			if err != nil {
				return name
			}
			name = text
		default:
			return name
		}
	}
	return name
}

func richChannelType(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "chan ") || strings.HasPrefix(text, "chan<-") || strings.HasPrefix(text, "<-chan") || text == "chan"
}
func richFunctionType(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "func(") || strings.HasPrefix(text, "func ")
}

// richResultOwner renders the revocable authority a frame's capabilities
// belong to. It is built from the explicit runtime context, never from a
// package-level convention.
func (e *emitter) richResultOwner(c RuntimeContext) (string, error) {
	if err := e.runtimeContext(c); err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	return "&" + rt + "ResultOwner{Channels: " + c.Channels + ", Session: " + c.Session + "}", nil
}

// richResultFrame renders the callee prolog's frame allocation. The frame is a
// local of the invocation: there is no ambient last-result slot, so nested and
// concurrent invocations never share one.
func (e *emitter) richResultFrame(name string, c RuntimeContext, plan richResultPlan) (string, error) {
	owner, err := e.richResultOwner(c)
	if err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	return name + " := " + rt + "MustValue(" + rt + "NewResultFrame(" + owner + ", " + strconv.Itoa(len(plan.Results)) + "))", nil
}

// richResultRecord renders one result's record at a return. A direct result
// keeps the declared conversion; a capability result stores the callee's
// declared value and retains the authority beside it.
func (e *emitter) richResultRecord(frame string, plan richResultPlan, index int, value string) (string, error) {
	if index < 0 || index >= len(plan.Results) {
		return "", e.fail(nil, CodeResult, fmt.Sprintf("result %d is outside the callable's %d results", index, len(plan.Results)))
	}
	descriptor := plan.Results[index]
	rt := e.prefix + "rt."
	position := strconv.Itoa(index)
	switch plan.Classes[index] {
	case richCapability:
		// The carrier keeps the callee's own result cell, or the zero of the
		// declared type when the result is unnamed. `*new(T)` spells that zero
		// for any carrier, an integer `Ch` and a `string` alike.
		carried := "*new(" + descriptor.Declared + ")"
		if descriptor.Name != "" {
			carried = e.goName(descriptor.Name)
		}
		// A scope refusal here is an ordinary channel diagnostic.
		return rt + "MustChannelOperation(" + rt + "SetResultCapability(" + frame + ", " + position + ", " + carried + ", " + value + "))", nil
	case richUnrepresentable:
		return "", e.fail(nil, CodeResult, fmt.Sprintf("result %d holds %s, which has no %s representation and no capability this runtime carries", index, descriptor.Payload, descriptor.Declared))
	}
	converted := value
	if descriptor.Declared != "" {
		converted = "(" + descriptor.Declared + ")(" + value + ")"
	}
	return rt + "MustResult(" + rt + "SetResult(" + frame + ", " + position + ", " + converted + "))", nil
}

// richResultTransfer renders the caller's transactional commit. Every target,
// every static type and every capability owner is validated before anything is
// written, so a rejected transfer leaves both the prior values and the prior
// capability bindings untouched.
func (e *emitter) richResultTransfer(frame, sidecars string, targets []string, site string) (string, error) {
	if sidecars == "" {
		return "", e.fail(nil, CodeBridge, "rich result transfer requires an explicit sidecar table")
	}
	rt := e.prefix + "rt."
	failure := e.prefix + "resultError"
	call := rt + "TransferResults(" + frame + ", " + sidecars + ", []any{" + strings.Join(targets, ",") + "}, " + site + ")"
	return "if " + failure + " := " + call + "; " + failure + " != nil {" + e.operationFailure(failure) + "}", nil
}

// richCapabilityReceive renders a receive that reads through the capability
// bound to a binding rather than through its declared value. The element type
// is the one the plan already knows, so the received value stays an ordinary
// typed Go value; the carrier is never parsed and never reinterpreted.
func (e *emitter) richCapabilityReceive(c RuntimeContext, sidecars, target, elem string) (string, error) {
	if err := e.richCapabilityOperand(c, sidecars, elem); err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	return rt + "MustReceiveValue(" + rt + "ReceiveCapability[" + elem + "](" + c.Context + ", " + sidecars + ", &" + target + "))", nil
}

// richCapabilitySend renders the other half of a carrier that holds authority:
// a send through the binding's channel, with the runtime's ordinary send
// diagnostics. Serialized text never reaches this seam; only a binding does.
func (e *emitter) richCapabilitySend(c RuntimeContext, sidecars, target, elem, value string) (string, error) {
	if err := e.richCapabilityOperand(c, sidecars, elem); err != nil {
		return "", err
	}
	rt := e.prefix + "rt."
	return rt + "MustChannelOperation(" + rt + "SendCapability[" + elem + "](" + c.Context + ", " + sidecars + ", &" + target + ", " + value + "))", nil
}

func (e *emitter) richCapabilityOperand(c RuntimeContext, sidecars, elem string) error {
	if err := e.runtimeContext(c); err != nil {
		return err
	}
	if sidecars == "" {
		return e.fail(nil, CodeBridge, "a capability operation requires an explicit sidecar table")
	}
	if elem == "" {
		return e.fail(nil, CodeType, "a capability operation requires the channel element type")
	}
	return nil
}

// richSidecarFork renders the table a child region uses. It goes through the
// same snapshot the child's storage was cloned with, and is checked against
// the child's own ownership scope, so a subshell refuses an inherited channel
// while a task that shares its owner's scope keeps it.
func (e *emitter) richSidecarFork(c RuntimeContext, parent, snapshot string) (string, error) {
	owner, err := e.richResultOwner(c)
	if err != nil {
		return "", err
	}
	if parent == "" || snapshot == "" {
		return "", e.fail(nil, CodeBridge, "a sidecar fork requires the parent table and the child snapshot")
	}
	rt := e.prefix + "rt."
	return rt + "MustValue(" + rt + "ForkSidecars(" + parent + ", " + snapshot + ", " + owner + "))", nil
}

// richNativeResult renders the public native wrapper's read of one result. A
// result that exists only as capability metadata is rejected there explicitly
// instead of escaping to an ordinary Go consumer as a fabricated zero.
func (e *emitter) richNativeResult(frame string, plan richResultPlan, index int, site string) (string, error) {
	if index < 0 || index >= len(plan.Results) {
		return "", e.fail(nil, CodeResult, fmt.Sprintf("result %d is outside the callable's %d results", index, len(plan.Results)))
	}
	rt := e.prefix + "rt."
	return rt + "MustValue(" + rt + "NativeResult[" + plan.Results[index].Declared + "](" + frame + ", " + strconv.Itoa(index) + ", " + site + "))", nil
}
