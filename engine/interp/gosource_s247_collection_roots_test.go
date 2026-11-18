// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Sprint: #247; Story: #671; Story-ID: 56d156f9118e
func TestS247LargeZeroSizeCollectionRoots(t *testing.T) {
	for _, name := range []string{"issue29190.go", "issue7550.go"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", name)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			differGoSource(t, string(source), nil, "")
		})
	}
}
