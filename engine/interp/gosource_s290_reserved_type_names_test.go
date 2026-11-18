//go:build full

package interp_test

// Sprint: #290; Story: #939; Story-ID: c7d8f9e96827
//
// A package-level type named like a dependency-helper identifier (`request`,
// `value`, `response`) crosses into imported packages like any other named
// type; a normally named struct is the control.

import "testing"

func TestGoSourceReservedHelperTypeNames(t *testing.T) {
	const decls = `package main
import (
	"encoding/json"
	"fmt"
	"strings"
)
type request struct {
	ID   string ` + "`json:\"instance_id\"`" + `
	Path string ` + "`json:\"repo_path,omitempty\"`" + `
}
type taskRequest struct {
	ID string ` + "`json:\"instance_id\"`" + `
}
type value int
func (v value) String() string { return fmt.Sprintf("value(%d)", int(v)) }
type response struct {
	R request ` + "`json:\"r\"`" + `
	V value   ` + "`json:\"v\"`" + `
}
var _ = strings.NewReader
var _ = json.Marshal
func main() {
`
	for name, tc := range map[string]struct{ body, want string }{
		"unmarshal": {`var r request
	err := json.Unmarshal([]byte(` + "`{\"instance_id\":\"x\",\"repo_path\":\"/r\"}`" + `), &r)
	fmt.Println(r.ID, r.Path, err)`, "x /r <nil>\n"},
		"decoder_disallow_unknown": {`var r request
	dec := json.NewDecoder(strings.NewReader(` + "`{\"instance_id\":\"y\"}`" + `))
	dec.DisallowUnknownFields()
	err := dec.Decode(&r)
	fmt.Println(r.ID, err)`, "y <nil>\n"},
		"marshal": {`b, err := json.Marshal(request{ID: "z", Path: "/p"})
	fmt.Println(string(b), err)`, "{\"instance_id\":\"z\",\"repo_path\":\"/p\"} <nil>\n"},
		"nested_reserved": {`b, err := json.Marshal(response{R: request{ID: "n"}, V: 4})
	fmt.Println(string(b), err)`, "{\"r\":{\"instance_id\":\"n\"},\"v\":4} <nil>\n"},
		// %T of a renamed type prints its generated helper identity (the
		// recorded fidelity gap); the value and its method cross intact.
		"defined_scalar_and_method": {`var v value = 2
	fmt.Println(value(3), v)`, "value(3) value(2)\n"},
		"control_normal_name": {`var r taskRequest
	err := json.Unmarshal([]byte(` + "`{\"instance_id\":\"c\"}`" + `), &r)
	b, _ := json.Marshal(r)
	fmt.Println(r.ID, string(b), err)`, "c {\"instance_id\":\"c\"} <nil>\n"},
	} {
		t.Run(name, func(t *testing.T) {
			out, errout, err := runGoSource(t, name, decls+"\t"+tc.body+"\n}\n")
			if err != nil || errout != "" || out != tc.want {
				t.Fatalf("out=%q want %q stderr=%q err=%v", out, tc.want, errout, err)
			}
		})
	}
}
