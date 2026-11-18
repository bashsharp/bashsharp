package interp

import (
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestLocalTypeDescriptorsTrackFileAndImports(t *testing.T) {
	parse := func(source string) *syntax.File {
		t.Helper()
		p, err := gosource.Parse(strings.NewReader(source), "types.go", gosource.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return p.File
	}
	r := &Runner{bashPPGoSource: true, bashPPGoSourceFile: parse("package main; import dep \"time\"; type Payload struct { Value dep.Time }\n"), bashPPImports: map[string]string{"dep": "time"}}
	for range 2 {
		got := r.bashPPLocalTypeDescriptors()
		if !slices.ContainsFunc(got, func(d bashPPLocalType) bool { return d.Name == "Payload" }) {
			t.Fatalf("imported field descriptor: %+v", got)
		}
	}
	// Copied toolchains share immutable descriptors, but use their own imports.
	child := &Runner{
		bashPPGoSource:     r.bashPPGoSource,
		bashPPGoSourceFile: r.bashPPGoSourceFile,
		bashPPImports:      nil,
		bashPPTools:        r.bashPPTools,
	}
	if got := child.bashPPLocalTypeDescriptors(); len(got) != 0 {
		t.Fatalf("unbound import materialized: %+v", got)
	}
	if got := r.bashPPLocalTypeDescriptors(); !slices.ContainsFunc(got, func(d bashPPLocalType) bool { return d.Name == "Payload" }) {
		t.Fatalf("child changed parent descriptors: %+v", got)
	}
	r.bashPPGoSourceFile = parse("package main; type Replacement struct { N int }\n")
	if got := r.bashPPLocalTypeDescriptors(); !slices.ContainsFunc(got, func(d bashPPLocalType) bool { return d.Name == "Replacement" }) || slices.ContainsFunc(got, func(d bashPPLocalType) bool { return d.Name == "Payload" }) {
		t.Fatalf("stale source descriptors: %+v", got)
	}
	r.bashPPGoSource = false
	if got := r.bashPPLocalTypeDescriptors(); got != nil {
		t.Fatalf("descriptors escaped Go source mode: %+v", got)
	}
}
