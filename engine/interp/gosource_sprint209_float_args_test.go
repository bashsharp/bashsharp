// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

func TestGoSourceSprint209ExactFloatArgumentCarriers(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

type F32 float32
type F64 float64

func use(a float64, b float32, c F64, d F32) {
	fmt.Printf("%.17g %.9g %.17g %.9g %T %T\n", a, b, c, d, c, d)
}

func main() {
	use(1.2, 2.5, 0.1, 1.2)
}
`, nil, "")
}
