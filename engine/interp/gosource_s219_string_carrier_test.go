//go:build full

package interp_test

// Sprint: #219; Story: #461; Story-ID: 4ed649697945

import "testing"

// String-producing reads stay scalar values even when the general read path
// also handles structured values. The first case is fixedbugs/issue19911's
// short declaration shape and also pins the defined string result type. The
// second case reads a string from dependency-owned indexed storage. Calls in
// the bounds, receiver and key expressions make the read-once rule observable.
func TestS219StringCarrier(t *testing.T) {
	cases := map[string]string{
		"slice_short_decl_named_string": `package main

import "strings"

var calls int
func separator(s string) int { calls++; return strings.Index(s, ": ") }

func main() {
	gotfull := "prefix: value"
	got := gotfull[separator(gotfull)+len(": "):]
	println(got == "value", got, calls)
}
`,
		"native_indexed_string": `package main

import "net/url"

var keyCalls int
func key() string { keyCalls++; return "better" }

func main() {
	values, err := url.ParseQuery("better=some+things+are+better")
	if err != nil { panic(err) }
	got := values[key()][0]
	println(got == "some things are better", got, keyCalls)
}
`,
		"named_string_map_lookup": `package main

type namedString string

var keyCalls int
func key() string { keyCalls++; return "answer" }

func main() {
	values := map[string]namedString{"answer": "yes"}
	got := values[key()]
	println(got == namedString("yes"), got, keyCalls)
}
`,
		"native_interface_map_string": `package main

import "encoding/json"

var keyCalls int
func key() string { keyCalls++; return "better" }

func main() {
	values := map[string]interface{}{}
	if err := json.Unmarshal([]byte("{\"better\":\"some things are better\"}"), &values); err != nil { panic(err) }
	got := values[key()]
	println(got == "some things are better", keyCalls)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
