//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Sprint: #243; Story: #672; Story-ID: fa5b3cf5a929
func TestS243OriginalGenericLayoutProvenance(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "typeparam", "issue47716.go"))
	if err != nil {
		t.Fatal(err)
	}
	typedSendThreeModes(t, string(source))
}
func TestS243StringLayoutProvenance(t *testing.T) {
	typedSendThreeModes(t, `package main
import "unsafe"
type S string
var calls int
func f() string {calls++; return "a"}
func main(){
 v:="abc"; empty:=""; var named S="a"; var boxed any=v
 if unsafe.Sizeof(v)!=16 || unsafe.Alignof(v)!=8 || unsafe.Sizeof(empty)!=16 || unsafe.Sizeof(named)!=16 || unsafe.Sizeof(boxed)!=16 {panic("layout")}
 if unsafe.Sizeof(f())!=16 || calls!=0 {panic("evaluation")}
}`)
}
