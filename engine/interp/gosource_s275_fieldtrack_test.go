//go:build full

package interp_test

import "testing"

func TestS275TrackedFieldCrossesBridge(t *testing.T) {
	src := "package main\n" +
		"import (\"fmt\"; \"reflect\")\n" +
		"type T struct { X int `go:\"track\"` }\n" +
		"func main() { v := T{X: 7}; fmt.Println(reflect.ValueOf(v).Field(0).Int()) }\n"
	out, stderr, err := runGoSource(t, "s275-tracked-field", src)
	if err != nil || out != "7\n" || stderr != "" {
		t.Fatalf("tracked field bridge: out=%q stderr=%q err=%v", out, stderr, err)
	}
}
