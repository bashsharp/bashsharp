// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoSourceSprint170ScalarDispatch(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type Count int8
type CountAlias = Count
type Octet uint8
type OctetAlias = Octet

func count() CountAlias { return -1 }
func octet() OctetAlias { return 255 }

	func main() {
		c, o := count(), octet()
		fmt.Printf("%T %d %T %d %t %t %q %q\n", c, c, o, o, c == -1, o == 255, string(c), string(o))
	}
`, nil, "")
}

func TestGoSourceSprint170WideScalarConversions(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "sprint170", "scalar-dispatch", "wide_uint.go"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(source), nil, "")
}
