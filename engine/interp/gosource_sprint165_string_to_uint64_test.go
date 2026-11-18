// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

const sprint165StringToUint64Root = "testdata/sprint165/string-to-uint64"

func TestGoSourceSprint165StringCarrierToUint64(t *testing.T) {
	source := mustReadSprint165StringToUint64(t, "runtime_float_carrier.go.txt")
	differGoSource(t, source, nil, "")
}

func mustReadSprint165StringToUint64(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sprint165StringToUint64Root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
