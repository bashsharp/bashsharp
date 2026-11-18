package interp

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
//
// Copy coherence for a read-only emitter walking original slices.
//
// fmt's formatting walk reads a decoded COPY of every original slice it is
// handed, while a formatting protocol method it invokes (String, Error,
// Format, GoString) runs against the interpreter's own storage. A callback
// that writes storage the walk has not printed yet would leave the copy
// stale, so prepareNativeSliceBuffers refused every such request outright.
//
// The walk itself never writes, and nothing outlives the call, so the copy
// stays exact for as long as no callback changes what the dependency holds.
// That is checked rather than assumed. The dependency's view is modelled as
// what the request sent, in canonical form: each origin-bearing pointer is
// spelled by its origin, and each origin's pointee is kept once in a table,
// because the worker binds every occurrence of one origin to one native
// pointee. After every callback the model takes the one update the worker
// really applies — the callback receiver's pointee, re-decoded from the reply
// into that same native pointee — and the live interpreter storage behind
// every argument is re-read and compared with the model. Any difference is a
// write the dependency cannot observe: the callback reply carries the failure
// before the walk goes on, and the program fails instead of printing a stale
// value.
//
// Only arguments whose live storage can be re-read are admitted: a direct
// original slice (its captured view), an origin-bearing pointer (its origin),
// or a struct or array containing those sources. Dependency-owned values and
// reference-free copies need no re-read. Maps, channels, functions, and pointers
// without an authenticated origin keep the refusal.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// goSourceCopyCoherence is the dependency's model of the storage a read-only
// request copied, and the live sources that storage is re-read from.
type goSourceCopyCoherence struct {
	sources []goSourceCopySource
	args    []string          // canonical argument spellings, by source
	pointee map[uint64]string // origin -> canonical pointee as the worker holds it
}

type goSourceCopySource struct {
	view   *bashPPNativeSlice // a direct original slice
	origin uint64             // or an origin-bearing pointer
}

// bridgeValueReferenceFree reports a value copy that shares no storage with
// its source: no slice, map, pointer, callback or origin anywhere inside.
func bridgeValueReferenceFree(v bashPPBridgeValue) bool {
	if v.Origin != 0 || v.sliceView != nil || v.Callbacks {
		return false
	}
	switch v.Kind {
	case "slice", "map", "pointer", "callback", "chan", "func":
		return false
	}
	for _, e := range v.Elements {
		if !bridgeValueReferenceFree(e) {
			return false
		}
	}
	for _, f := range v.Fields {
		if !bridgeValueReferenceFree(f) {
			return false
		}
	}
	return len(v.Entries) == 0
}

// prepareNativeCopyCoherence admits a read-only request whose every argument
// has a re-readable live source, recording the dependency model on q.
func prepareNativeCopyCoherence(req bashPPEvalRequest, q *bashPPBridgeRequest) bool {
	q.coherence = nil
	if q.Op != "call" || req.CallbackOwner == nil || req.Bridge == nil {
		return false
	}
	if q.Receiver != nil && !bridgeValueDependencyOwned(*q.Receiver) {
		return false
	}
	c := &goSourceCopyCoherence{pointee: map[uint64]string{}}
	for _, arg := range q.Args {
		if !c.collectSources(arg, req) {
			return false
		}
	}
	if len(c.sources) == 0 {
		return false
	}
	q.coherence = c
	return true
}

// collectSources walks one argument and records every re-readable live source
// it holds: a slice (its captured backing view), an origin-bearing pointer (its
// origin). A slice or origin pointer is re-read whole, so its contents are not
// walked again. A struct or array value is opened field by field and element by
// element, so a slice nested inside a value receiver — the shape a fmt %v walk
// hands an ir node — is tracked exactly as a direct slice argument is. A leaf
// that shares no interpreter storage (a scalar, a dependency handle) contributes
// no source. Anything whose live storage cannot be re-read — a map, a channel, a
// function, a pointer without an authenticated origin — keeps the request out of
// the coherence path so it stays refused.
func (c *goSourceCopyCoherence) collectSources(v bashPPBridgeValue, req bashPPEvalRequest) bool {
	switch {
	case v.sliceView != nil:
		c.sources = append(c.sources, goSourceCopySource{view: v.sliceView})
	case v.Kind == "pointer" && v.Origin != 0 && v.Session == req.Bridge.id:
		c.sources = append(c.sources, goSourceCopySource{origin: v.Origin})
	case bridgeValueDependencyOwned(v) || bridgeValueReferenceFree(v):
		return true
	case v.Kind == "struct":
		for _, field := range v.Fields {
			if !c.collectSources(field, req) {
				return false
			}
		}
		return true
	case v.Kind == "array":
		for _, e := range v.Elements {
			if !c.collectSources(e, req) {
				return false
			}
		}
		return true
	default:
		return false
	}
	spelled, ok := goSourceCanonicalValue(v, c.pointee)
	if !ok {
		return false
	}
	c.args = append(c.args, spelled)
	return true
}

// afterCallback applies the receiver reconciliation the worker performs and
// then requires the live storage to match the dependency's model.
func (c *goSourceCopyCoherence) afterCallback(r *Runner, receiver *bashPPBridgeValue) error {
	if receiver != nil {
		updated := map[uint64]string{}
		if _, ok := goSourceCanonicalValue(*receiver, updated); !ok {
			return fmt.Errorf("gosource: a callback receiver has no canonical spelling")
		}
		for origin, spelled := range updated {
			c.pointee[origin] = spelled
		}
	}
	session := r.bashPPTools.bridge
	if session == nil {
		return fmt.Errorf("gosource: copy coherence lost its dependency session")
	}
	live := map[uint64]string{}
	for i, source := range c.sources {
		var value bashPPBridgeValue
		var err error
		if source.view != nil {
			value, err = r.bashPPBridgeCollection(source.view.view, source.view.meta, source.view.typ)
		} else {
			ptr, _ := session.originLookup(source.origin)
			if ptr == nil {
				return fmt.Errorf("gosource: copied pointer identity expired during a callback")
			}
			value, err = r.bashPPBridgePointerValue(ptr)
		}
		if err != nil {
			return err
		}
		spelled, ok := goSourceCanonicalValue(value, live)
		if !ok || spelled != c.args[i] {
			return errGoSourceStaleCopy
		}
	}
	for origin, spelled := range live {
		if held, ok := c.pointee[origin]; !ok || held != spelled {
			return errGoSourceStaleCopy
		}
	}
	return nil
}

var errGoSourceStaleCopy = fmt.Errorf("gosource: an original callback wrote storage the dependency holds a copy of; the copy would be stale")

// goSourceCanonicalValue spells v with every origin-bearing pointer replaced
// by its origin, recording each origin's pointee in table. A value that names
// one origin with two different pointees has no single native counterpart
// and reports false.
func goSourceCanonicalValue(v bashPPBridgeValue, table map[uint64]string) (string, bool) {
	var b strings.Builder
	ok := goSourceCanonicalInto(&b, v, table)
	return b.String(), ok
}

func goSourceCanonicalInto(b *strings.Builder, v bashPPBridgeValue, table map[uint64]string) bool {
	if v.Kind == "pointer" && v.Origin != 0 {
		b.WriteString("@")
		b.WriteString(strconv.FormatUint(v.Origin, 10))
		if len(v.Elements) == 0 {
			return true // a back-reference to a pointee spelled elsewhere
		}
		if len(v.Elements) != 1 {
			return false
		}
		pointee, ok := goSourceCanonicalValue(v.Elements[0], table)
		if !ok {
			return false
		}
		if held, seen := table[v.Origin]; seen && held != pointee {
			return false
		}
		table[v.Origin] = pointee
		return true
	}
	b.WriteString(strconv.Quote(v.Kind))
	b.WriteString(strconv.Quote(v.Type))
	b.WriteString(strconv.Quote(v.Text))
	b.WriteString(strconv.Quote(string(v.Bytes)))
	b.WriteString(strconv.FormatUint(v.Handle, 10))
	if v.Origin != 0 {
		b.WriteString("^" + strconv.FormatUint(v.Origin, 10))
	}
	b.WriteString("[")
	for _, e := range v.Elements {
		if !goSourceCanonicalInto(b, e, table) {
			return false
		}
		b.WriteString(",")
	}
	b.WriteString("]{")
	names := make([]string, 0, len(v.Fields))
	for name := range v.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString(strconv.Quote(name) + ":")
		if !goSourceCanonicalInto(b, v.Fields[name], table) {
			return false
		}
		b.WriteString(",")
	}
	b.WriteString("}<")
	entries := make([]string, 0, len(v.Entries))
	for _, e := range v.Entries {
		var entry strings.Builder
		if !goSourceCanonicalInto(&entry, e.Key, table) {
			return false
		}
		entry.WriteString("=")
		if !goSourceCanonicalInto(&entry, e.Value, table) {
			return false
		}
		entries = append(entries, entry.String())
	}
	sort.Strings(entries)
	b.WriteString(strings.Join(entries, ","))
	b.WriteString(">")
	return true
}
