//go:build full

package interp

import (
	"testing"
)

// The production depth bound is dependencyCallbackProofDepthBound and this lane
// does not change it. This measurement answers the operator's question
// that the bound alone cannot: how deep would the whole-package proof have had
// to descend if the only remedy had been a larger bound? It raises the bound in
// a test-only proof, drives the enumerate hook so a refusal does not truncate
// the walk, and reports the deepest body actually entered. It asserts nothing
// about the verdict: it is a report.
const dependencyCallbackDepthMeasurementBound = 4096

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281CallbackProofDepthMeasurement(t *testing.T) {
	for _, test := range []struct {
		name string
		fn   string
		args []int
	}{
		{name: "Parse", fn: "Parse", args: []int{2}},
		{name: "ParseFile", fn: "ParseFile", args: []int{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fset, files, _ := dependencyCallbackEnumerationFiles(t, nil)
			for _, bound := range []struct {
				name  string
				depth int
			}{
				{name: "production-bound", depth: dependencyCallbackProofDepthBound},
				{name: "raised-bound", depth: dependencyCallbackDepthMeasurementBound},
			} {
				enumeration := &dependencyCallbackEnumeration{sites: make(map[string]*dependencyCallbackEnumerationSite)}
				proof := newDependencyCallbackProof(files)
				proof.fset = fset
				proof.stepLimit = dependencyCallbackEnumerationStepBudget
				proof.depthLimit = bound.depth
				proof.enumerate = enumeration.record
				proof.prove(test.fn, test.args)
				t.Logf("syntax.%s %s: depthLimit=%d maxDepth=%d steps=%d refusals=%d sites=%d",
					test.fn, bound.name, bound.depth, proof.maxDepth, proof.steps, enumeration.total, len(enumeration.sites))
			}
		})
	}
}
