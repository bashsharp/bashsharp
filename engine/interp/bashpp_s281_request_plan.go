// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// A native request crosses the bridge once per imported operation the
// interpreted program performs, so a program in a long loop raises thousands of
// them against one live dependency session. Everything a request says about the
// program itself — the materialised local type namespace, the registered
// imported instantiations, the generic bridge types, the referenced selectors,
// the embed and companion declarations, the root files — is a function of the
// loaded source file, the import bindings and the directories the loader
// resolved them against. None of those change while a request runs.
//
// Those descriptor sets were nevertheless rebuilt per request, and each request
// then re-derived the session drift fingerprints and the local type lookups
// from them by scanning the whole set again. Both are O(program size), so one
// native call cost about 0.1ms in a four-declaration program and 2.7ms in a
// thousand-declaration one; a program the size of go/ssa never finished.
//
// The program-derived request shape, the fingerprints and the lookups are
// derived once per revision of those inputs here, keyed by them so a replaced
// source file or a newly executed import rebuilds, and shared with copied
// Runners through the by-value bashPPToolchain.

import (
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPLocalTypePlan is the derivation of one immutable local type namespace
// that every native request needs: the session drift fingerprint, the lookups
// a request performs by transported type name, and the whole-namespace
// predicates it asks about. It is immutable once published and safe to share
// with copied Runners and with the session's own goroutines.
type bashPPLocalTypePlan struct {
	types    []bashPPLocalType
	identity string
	// byName buckets descriptors under their own declared name and
	// byTransport additionally under the instantiation spelling a value of a
	// materialised generic is transported as. Both keep declaration order, so
	// a lookup applies the caller's own predicate to exactly the candidates
	// its former linear scan visited, in the same order.
	byName      map[string][]*bashPPLocalType
	byTransport map[string][]*bashPPLocalType
	// callbackNames are the transported spellings whose descriptor mirrors an
	// original method, in every form the helper may present one; see
	// requestHasCallbacks.
	callbackNames map[string]bool
	// methodsMirrored reports that the helper mirrors an original method at
	// all, and fmtFormatting that some descriptor mirrors or omits an fmt
	// formatting protocol method.
	methodsMirrored bool
	fmtFormatting   bool
	// scans is the program's descriptor-scan instrument, carried so a request
	// that has to derive its own plan is counted too.
	scans *atomic.Int64
}

// bashPPBuildLocalTypePlan derives the request-time lookups of one local type
// namespace. It is the only whole-namespace scan the request path performs.
func bashPPBuildLocalTypePlan(types []bashPPLocalType, scans *atomic.Int64) *bashPPLocalTypePlan {
	plan := &bashPPLocalTypePlan{
		types:         types,
		identity:      bashPPLocalTypeIdentity(types),
		byName:        make(map[string][]*bashPPLocalType, len(types)),
		byTransport:   make(map[string][]*bashPPLocalType, len(types)),
		callbackNames: map[string]bool{},
		scans:         scans,
	}
	if scans != nil {
		scans.Add(int64(len(types)))
	}
	for i := range types {
		local := &types[i]
		plan.byName[local.Name] = append(plan.byName[local.Name], local)
		plan.byTransport[local.Name] = append(plan.byTransport[local.Name], local)
		if local.WireType != "" && local.WireType != local.Name {
			plan.byTransport[local.WireType] = append(plan.byTransport[local.WireType], local)
		}
		for _, method := range local.Methods {
			if fmtProtocolMethod(method.Name) {
				plan.fmtFormatting = true
			}
		}
		for _, name := range local.OmittedMethods {
			if fmtProtocolMethod(name) {
				plan.fmtFormatting = true
			}
		}
		if len(local.Methods) == 0 {
			continue
		}
		plan.methodsMirrored = true
		plan.callbackNames[local.Name] = true
		// An instantiated generic type is materialised under a generated name
		// but transported under its instantiation spelling; recognise both, so
		// a value carrying its mirrored method is still seen as a callback.
		if local.WireType != "" {
			plan.callbackNames[local.WireType] = true
			// The helper spells type arguments without separator spaces,
			// while WireType preserves the source spelling. Match both forms
			// so a generic method callback keeps its owning request parked.
			if parsed := syntax.BashPPTypeExprFromText(local.WireType); parsed != nil {
				plan.callbackNames[bashPPBridgeTypeText(parsed)] = true
			}
		}
	}
	return plan
}

// sameLocalTypeSet reports whether two descriptor slices are the same published
// storage. A published set is immutable, so identical storage is identical
// content; a request assembled without a plan, or with a namespace the plan was
// not derived from, falls back to deriving one.
func sameLocalTypeSet(a, b []bashPPLocalType) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}

// localTypePlan is the derivation of this request's local type namespace.
func (req bashPPEvalRequest) localTypePlan() *bashPPLocalTypePlan {
	if plan := req.localPlan; plan != nil && sameLocalTypeSet(plan.types, req.LocalTypes) {
		return plan
	}
	var scans *atomic.Int64
	if req.CallbackOwner != nil {
		scans = req.CallbackOwner.bashPPTools.requestScans
	} else if req.localPlan != nil {
		scans = req.localPlan.scans
	}
	return bashPPBuildLocalTypePlan(req.LocalTypes, scans)
}

// bashPPSessionIdentity fingerprints the program-derived descriptor sets a live
// dependency session was generated from. A request whose fingerprint differs
// describes a different program than the running helper implements, and
// [bashPPNativeSession.begin] refuses it.
type bashPPSessionIdentity struct {
	imports    string
	locals     string
	embeds     string
	companions string
	cgo        string
	instances  string
	generics   string
	selectors  string
}

func bashPPBuildSessionIdentity(req bashPPEvalRequest, locals string) bashPPSessionIdentity {
	return bashPPSessionIdentity{
		imports: bridgeImportIdentity(req.Imports),
		locals:  locals,
		embeds:  bashPPEmbedIdentity(req.EmbedDecls),
		companions: bashPPNativeCompanionIdentity(req.CompanionFiles, req.NativeFuncs,
			req.MappedCompanions, req.CompanionTrampolines, req.CompanionUnmappedFrames),
		cgo:       bashPPCgoIdentity(req.CgoPackages),
		instances: bashPPImportedInstanceIdentity(req.Instances),
		generics:  bashPPGenericTypeIdentity(req.GenericTypes),
		selectors: strings.Join(req.Selectors, "\x00"),
	}
}

// sessionIdentity is this request's session fingerprint, derived once per
// program revision by [Runner.bashPPNativeRequestShape].
func (req bashPPEvalRequest) sessionIdentity() bashPPSessionIdentity {
	if req.identity != nil && req.identityPlan.matchesRequest(req) {
		return *req.identity
	}
	return bashPPBuildSessionIdentity(req, bashPPLocalTypeIdentity(req.LocalTypes))
}

// matchesRequest reports whether req still carries the immutable descriptor
// sets from this plan. A request is a value and package-internal callers may
// replace one of its registry fields after construction; such a copy must not
// retain the plan's cached fingerprint.
func (plan *bashPPNativeRequestPlan) matchesRequest(req bashPPEvalRequest) bool {
	return plan != nil && maps.Equal(plan.imports, req.Imports) &&
		samePublishedSlice(plan.localTypes, req.LocalTypes) &&
		samePublishedSlice(plan.embedDecls, req.EmbedDecls) &&
		samePublishedSlice(plan.companionFiles, req.CompanionFiles) &&
		samePublishedSlice(plan.nativeFuncs, req.NativeFuncs) &&
		samePublishedSlice(plan.mapped, req.MappedCompanions) &&
		samePublishedSlice(plan.trampolines, req.CompanionTrampolines) &&
		samePublishedSlice(plan.unmappedFrames, req.CompanionUnmappedFrames) &&
		samePublishedSlice(plan.cgo, req.CgoPackages) &&
		samePublishedSlice(plan.instances, req.Instances) &&
		samePublishedSlice(plan.genericTypes, req.GenericTypes) &&
		samePublishedSlice(plan.selectors, req.Selectors)
}

func samePublishedSlice[S ~[]E, E any](a, b S) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// bashPPNativeRequestPlan is the program-derived part of a native eval request:
// the descriptor sets the dependency helper is generated from, their session
// fingerprint, and the local type namespace derivation.
//
// Its key is every input those pieces read. The descriptor sets themselves are
// lexical — they are read from the loaded program rather than from the
// declarations executed so far, because the helper's materialised namespace is
// fixed for the life of the session that mirrors it — so the frame a request is
// raised in is not an input. What they are read THROUGH is: each builder
// memoises its own result on the toolchain under its own key, and the plan is a
// derivation of those memos. Naming the memos in the key therefore makes it
// complete by construction rather than by enumerating the writes that
// invalidate them: a memo rebuilt for any reason, now or after a later change,
// is a different memo and rebuilds the plan. See bashPPNativeRequestShape.
type bashPPNativeRequestPlan struct {
	file      *syntax.File
	goSource  bool
	imports   map[string]string
	dir       string
	moduleDir string
	// The memoised derivations this plan was read through. Compared by
	// identity: a published memo is immutable, so the same memo is the same
	// content, and a rebuilt one cannot be served from a plan derived from its
	// predecessor. This is what keeps the descriptors a request carries
	// coherent with the ones the rest of the request path
	// ([Runner.bashPPScopedLocalTypeName], the transport spellings) reads
	// directly from the toolchain while that request is assembled.
	localTypeMemo *bashPPLocalTypeCache
	declMemo      *bashPPLocalTypeDeclCache
	metadataMemo  *bashPPBridgeMetadataCache
	instanceMemo  *bashPPInstantiationIndex
	// scopeDependent marks the one derivation that is NOT lexical: a companion
	// signature naming a type declared inside a generic function is spelled
	// with the type parameters the running frame binds
	// ([Runner.bashPPNestTypeName]). It is only possible in a program that has
	// both native companions and such a declaration, and typeArgs is the
	// frame's bindings that plan was derived under. For every other program
	// the bindings are not an input and are never even fingerprinted, so the
	// reuse check stays O(imports) rather than O(scope).
	scopeDependent bool
	typeArgs       string

	sourceFile string
	sourceDir  string
	embedDecls []bashPPEmbedDecl
	rootFiles  []string
	cgo        []syntax.CgoPackage

	companionFiles []string
	nativeFuncs    []bashPPNativeFuncDecl
	trampolines    []bashPPCompanionTrampoline
	unmappedFrames []string

	mapped []bashPPMappedCompanion
	// err is the refusal the companion review reached for these inputs. It is
	// as reproducible as the inputs are, so it is reported to every request
	// the plan serves rather than only to the first.
	err error

	instances    []bashPPImportedInstance
	localTypes   []bashPPLocalType
	localPlan    *bashPPLocalTypePlan
	genericTypes []string
	selectors    []string

	identity bashPPSessionIdentity
}

// reusableIn reports whether this plan still describes the program r is
// running. Every comparison is O(1) but the import map, which is O(imports),
// and the frame's type bindings, which are only read for the rare program whose
// companion spellings depend on them.
func (plan *bashPPNativeRequestPlan) reusableIn(r *Runner) bool {
	if plan == nil {
		return false
	}
	if plan.file != r.bashPPGoSourceFile || plan.goSource != r.bashPPGoSource ||
		plan.dir != r.Dir || plan.moduleDir != r.bashPPTools.moduleDir {
		return false
	}
	if plan.localTypeMemo != r.bashPPTools.localTypes || plan.declMemo != r.bashPPTools.localTypeDecls ||
		plan.metadataMemo != r.bashPPTools.bridgeMetadata || plan.instanceMemo != r.bashPPTools.instantiations {
		return false
	}
	if plan.scopeDependent && plan.typeArgs != bashPPTypeArgsIdentity(r.bashPPTypeParamArgs) {
		return false
	}
	return maps.Equal(plan.imports, r.bashPPImports)
}

// bashPPTypeArgsIdentity fingerprints the type parameter bindings of the
// running frame, in name order. It is O(the frame's type parameters) — never
// O(program) — and is only computed for a plan whose companion spellings can
// read them.
func bashPPTypeArgsIdentity(args map[string]syntax.BashPPTypeExpr) string {
	if len(args) == 0 {
		return ""
	}
	names := slices.Sorted(maps.Keys(args))
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		if arg := args[name]; arg != nil {
			b.WriteString(bashPPTypeText(arg))
		}
		b.WriteByte(';')
	}
	return b.String()
}

// bashPPNativeRequestShape derives the program-dependent part of this runner's
// native requests, reusing the last derivation while its inputs are unchanged.
func (r *Runner) bashPPNativeRequestShape() *bashPPNativeRequestPlan {
	if plan := r.bashPPTools.requestPlan; plan.reusableIn(r) {
		if bashPPRequestPlanTrace != nil {
			bashPPRequestPlanTrace(r, plan, true)
		}
		return plan
	}
	plan := &bashPPNativeRequestPlan{
		file:      r.bashPPGoSourceFile,
		goSource:  r.bashPPGoSource,
		imports:   maps.Clone(r.bashPPImports),
		dir:       r.Dir,
		moduleDir: r.bashPPTools.moduleDir,
		typeArgs:  bashPPTypeArgsIdentity(r.bashPPTypeParamArgs),
	}
	plan.embedDecls, plan.sourceDir = r.bashPPGoSourceEmbedRequest()
	if r.bashPPGoSource && plan.sourceDir == "" {
		plan.sourceDir = r.bashPPGoSourceSourceDir()
	}
	plan.sourceFile = r.bashPPGoSourceSourceFile()
	plan.rootFiles = r.bashPPGoSourceRootFiles()
	plan.cgo = r.bashPPGoSourceCgoPackages()
	var err error
	plan.companionFiles, plan.nativeFuncs, plan.trampolines, plan.unmappedFrames, err = r.bashPPGoSourceNativeCompanions(plan.sourceDir)
	if err == nil {
		plan.mapped, err = r.bashPPGoSourceMappedCompanions()
	}
	plan.err = err
	plan.localTypes = r.bashPPLocalTypeDescriptors()
	plan.localPlan = bashPPBuildLocalTypePlan(plan.localTypes, r.bashPPTools.requestScans)
	plan.instances = r.bashPPImportedInstances()
	plan.genericTypes = r.bashPPGenericBridgeTypes()
	plan.selectors = r.bashPPReferencedSelectors()
	plan.identity = bashPPSessionIdentity{
		imports: bridgeImportIdentity(plan.imports),
		locals:  plan.localPlan.identity,
		embeds:  bashPPEmbedIdentity(plan.embedDecls),
		companions: bashPPNativeCompanionIdentity(plan.companionFiles, plan.nativeFuncs,
			plan.mapped, plan.trampolines, plan.unmappedFrames),
		cgo:       bashPPCgoIdentity(plan.cgo),
		instances: bashPPImportedInstanceIdentity(plan.instances),
		generics:  bashPPGenericTypeIdentity(plan.genericTypes),
		selectors: strings.Join(plan.selectors, "\x00"),
	}
	// The memos every piece above was read through, recorded after the
	// derivation populated them, and whether a companion spelling could have
	// read the frame's type bindings on the way.
	plan.localTypeMemo, plan.declMemo = r.bashPPTools.localTypes, r.bashPPTools.localTypeDecls
	plan.metadataMemo, plan.instanceMemo = r.bashPPTools.bridgeMetadata, r.bashPPTools.instantiations
	plan.scopeDependent = plan.companionsDeclared() && plan.localTypeMemo != nil && len(plan.localTypeMemo.nest) > 0
	r.bashPPTools.requestPlan = plan
	if bashPPRequestPlanTrace != nil {
		bashPPRequestPlanTrace(r, plan, false)
	}
	return plan
}

// named are the descriptors declared under name, in declaration order, so a
// caller applies its own predicate to the same candidates its former scan of
// the whole namespace visited.
func (plan *bashPPLocalTypePlan) named(name string) []*bashPPLocalType {
	if plan == nil {
		return nil
	}
	return plan.byName[name]
}

// transported are the descriptors a value arriving under name may be one of:
// the declarations of that name, plus a materialised generic whose
// instantiation spelling it is transported as.
func (plan *bashPPLocalTypePlan) transported(name string) []*bashPPLocalType {
	if plan == nil {
		return nil
	}
	return plan.byTransport[name]
}

// declared is the descriptor a name resolves to when the request path reads the
// namespace as a map from declared name to descriptor: the last declaration
// wins, exactly as the per-request map build it replaces did.
func (plan *bashPPLocalTypePlan) declared(name string) (bashPPLocalType, bool) {
	if plan == nil {
		return bashPPLocalType{}, false
	}
	bucket := plan.byName[name]
	if len(bucket) == 0 {
		return bashPPLocalType{}, false
	}
	return *bucket[len(bucket)-1], true
}

// localMethodCallbackName reports whether a value transported under name
// carries a mirrored original method, in any spelling the helper may present.
func (plan *bashPPLocalTypePlan) localMethodCallbackName(typeName string) bool {
	if plan == nil {
		return false
	}
	return plan.callbackNames[strings.TrimPrefix(strings.TrimPrefix(typeName, "*"), "main.")]
}

// bashPPRequestPlanTrace observes every plan derivation the request path makes:
// reused reports that the cached plan answered, so a test can compare what the
// request was handed against a derivation made at that exact point of
// execution. Nil outside tests; see bashpp_s281_request_plan_internal_test.go.
var bashPPRequestPlanTrace func(r *Runner, plan *bashPPNativeRequestPlan, reused bool)

// companionsDeclared reports whether this program has any native companion
// declaration at all. Only such a program can have a companion spelling, and
// therefore only such a program can read the running frame's type bindings
// while its request is assembled.
func (plan *bashPPNativeRequestPlan) companionsDeclared() bool {
	return len(plan.nativeFuncs) > 0 || len(plan.trampolines) > 0 || len(plan.mapped) > 0
}
