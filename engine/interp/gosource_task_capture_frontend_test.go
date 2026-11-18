// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Precision of the capture set, measured on the representation the GoSource
// frontend actually produces.
//
// The raw Bash++ parser leaves most positioned expressions empty outside a
// committed Go region, so a unit test built on it can only exercise the legacy
// word surface — and would pass for shapes the walker never saw. These cases
// therefore go through gosource.Parse, which is the frontend a real original Go
// program is analysed through. Each program declares outer variables and
// asserts EXACTLY which of their cells the launched task shares: a missing one
// is Go's by-reference capture lost, an extra one is a live parent variable
// spliced into a concurrently running task for no reason the program gave.

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceGoStmt returns the first `go` statement of a real Go program, as the
// frontend lowers it.
func goSourceGoStmt(t *testing.T, source string) *syntax.BashPPGo {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), "capture.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var found *syntax.BashPPGo
	syntax.Walk(program.File, func(node syntax.Node) bool {
		if g, ok := node.(*syntax.BashPPGo); ok && found == nil {
			found = g
		}
		return true
	})
	if found == nil {
		t.Fatal("no go statement in program")
	}
	return found
}

func TestGoSourceCaptureFrontendPrecision(t *testing.T) {
	cases := []struct {
		name   string
		source string
		outer  []string
		want   []string
	}{{
		name: "string_literal_text_is_not_a_use",
		source: `package main

import "fmt"

func main() {
	counter := 0
	total := 0
	done := make(chan bool)
	go func() {
		counter++
		fmt.Println("total", counter)
		done <- true
	}()
	<-done
	fmt.Println(total)
}
`,
		outer: []string{"counter", "total"},
		want:  []string{"counter"},
	}, {
		name: "short_decl_initializer_reads_the_outer_name",
		source: `package main

import "fmt"

func main() {
	x := 1
	done := make(chan bool)
	go func() {
		x := x + 1
		fmt.Println(x)
		done <- true
	}()
	<-done
}
`,
		outer: []string{"x"},
		want:  []string{"x"},
	}, {
		name: "local_shadow_leaves_the_outer_cell_copied",
		source: `package main

import "fmt"

func main() {
	x := 1
	y := 2
	done := make(chan bool)
	go func() {
		var x int
		x++
		fmt.Println(x, y)
		done <- true
	}()
	<-done
	fmt.Println(x)
}
`,
		outer: []string{"x", "y"},
		want:  []string{"y"},
	}, {
		name: "selector_root_only",
		source: `package main

import "fmt"

type Config struct{ Timeout int }

func main() {
	cfg := Config{Timeout: 1}
	Timeout := 2
	done := make(chan bool)
	go func() {
		fmt.Println(cfg.Timeout)
		done <- true
	}()
	<-done
	fmt.Println(Timeout)
}
`,
		outer: []string{"cfg", "Timeout"},
		want:  []string{"cfg"},
	}, {
		name: "struct_field_key_is_not_a_use",
		source: `package main

import "fmt"

type Point struct{ X int }

func main() {
	X := 1
	n := 2
	done := make(chan bool)
	go func() {
		p := Point{X: n}
		fmt.Println(p)
		done <- true
	}()
	<-done
	fmt.Println(X)
}
`,
		outer: []string{"X", "n"},
		want:  []string{"n"},
	}, {
		name: "local_slice_and_map_capture_only_their_operands",
		source: `package main

import "fmt"

func main() {
	seed := 1
	key := "k"
	xs := []int{9}
	done := make(chan bool)
	go func() {
		local := []int{seed}
		m := map[string]int{key: seed}
		fmt.Println(local, m)
		done <- true
	}()
	<-done
	fmt.Println(xs)
}
`,
		outer: []string{"seed", "key", "xs"},
		want:  []string{"seed", "key"},
	}, {
		name: "parameter_shadows_and_argument_is_copied",
		source: `package main

import "fmt"

func main() {
	n := 1
	shared := 0
	done := make(chan bool)
	go func(n int) {
		shared += n
		fmt.Println(n)
		done <- true
	}(n)
	<-done
	fmt.Println(shared)
}
`,
		outer: []string{"n", "shared"},
		want:  []string{"shared"},
	}, {
		name: "nested_closure_free_variable_is_free_in_the_body",
		source: `package main

import "fmt"

func main() {
	counter := 0
	p := 0
	done := make(chan bool)
	go func() {
		inner := func(p int) int {
			counter += p
			return p
		}
		fmt.Println(inner(1))
		done <- true
	}()
	<-done
	fmt.Println(counter, p)
}
`,
		outer: []string{"counter", "p"},
		want:  []string{"counter"},
	}, {
		name: "unicode_identifiers_are_identifiers",
		source: `package main

import "fmt"

func main() {
	计数器 := 0
	naïve := 0
	done := make(chan bool)
	go func() {
		计数器++
		fmt.Println(计数器)
		done <- true
	}()
	<-done
	fmt.Println(naïve)
}
`,
		outer: []string{"计数器", "naïve"},
		want:  []string{"计数器"},
	}, {
		name: "captured_pointer_target",
		source: `package main

import "fmt"

func main() {
	n := 0
	p := &n
	other := 0
	done := make(chan bool)
	go func() {
		*p = 7
		done <- true
	}()
	<-done
	fmt.Println(n, other)
}
`,
		outer: []string{"p", "other"},
		want:  []string{"p"},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := goSourceGoStmt(t, tc.source)
			r, cells := captureRunnerFor(tc.outer...)
			shared, _ := r.bashPPGoSourceTaskCapture(g.Call)
			want := make(map[string]bool, len(tc.want))
			for _, name := range tc.want {
				want[name] = true
			}
			for _, name := range tc.outer {
				got := shared[cells[name]]
				if got != want[name] {
					t.Errorf("%q shared=%v, want %v (shared set has %d cells)", name, got, want[name], len(shared))
				}
			}
		})
	}
}
