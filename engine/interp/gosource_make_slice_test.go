// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceMakeSliceFaults(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "gosource-make-slice", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			differGoSource(t, string(source), nil, "")
		})
	}
}
func TestGoSourceMakeSliceInvalidConstants(t *testing.T) {
	for _, expr := range []string{"make([]int,-1)", "make([]int,0,-1)", "make([]int,2,1)", "make([]int,uint64(1)<<63)"} {
		source := "package main;func main(){_=" + expr + "}"
		if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
			t.Fatalf("accepted %s", expr)
		}
	}
}
func TestGoSourceMakeSliceZeroSizeLogicalRepresentation(t *testing.T) {
	source := `package main
func main(){n:=int(1<<59);s:=make([]struct{},0,n);if len(s)!=0 || cap(s)!=n {panic("size")}}`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if want := runNativeOracle(t, dir, path, nil, ""); want.status != 0 {
		t.Fatalf("native=%+v", want)
	}
	if got := runGoSourceRunner(t, dir, path, source, nil, ""); got.status != 0 || got.stderr != "" {
		t.Fatalf("Runner=%+v", got)
	}
}
