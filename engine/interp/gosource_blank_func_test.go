//go:build full

package interp_test

// Sprint: #270; Story: #756; Story-ID: a0503101d15a

import "testing"

// Go's blank identifier declares no function, so independently generated
// stringer files in one package can each use `func _`. Keep this source-only
// equivalent portable by comparing the interpreter with the active Go oracle.
func TestGoSourceBlankFunctionsDoNotBind(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

func _() { panic("not callable") }
func _() { panic("also not callable") }

func main() { fmt.Println("ok") }
`, nil, "")
}
