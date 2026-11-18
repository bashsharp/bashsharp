// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #279; Story: #757; Story-ID: 609c89bfa598

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPS279BasicLiteralBridging verifies that basic literals of all Go
// spellings (including hex 0x2, underscored 1_0, octal 077, binary 0b101,
// floats, characters, strings, and booleans) correctly bridge to native worker
// calls without normalization errors or strconv.ParseInt failures.
func TestBashPPS279BasicLiteralBridging(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"strconv"
)

func main() {
	s1 := strconv.Itoa(0x2)
	s2 := strconv.Itoa(1_0)
	s3 := strconv.Itoa(077)
	s4 := strconv.Itoa(0b101)
	formatted := fmt.Sprintf("%s,%s,%s,%s,%s,%g,%c,%t,%t",
		s1, s2, s3, s4,
		"dwarf-世界",
		1.5,
		'z',
		true,
		false,
	)
	println(formatted)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "s279_lit_bridge.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, stderr.String())
	}
	const want = "2,10,63,5,dwarf-世界,1.5,z,true,false"
	got := strings.TrimSpace(stderr.String())
	if got == "" {
		got = strings.TrimSpace(stdout.String())
	}
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashPPS279StringComparisonSemantics guards against string ordering
// regressions across bridge representations, verifying that cmp.Compare and
// slices.SortFunc maintain canonical string comparison semantics.
func TestBashPPS279StringComparisonSemantics(t *testing.T) {
	const source = `package main
import (
	"cmp"
	"fmt"
	"slices"
)

type Item struct {
	name string
}

func main() {
	c1 := cmp.Compare("alpha", "beta")
	c2 := cmp.Compare("beta", "alpha")
	c3 := cmp.Compare("same", "same")

	items := []*Item{{name: "gamma"}, {name: "alpha"}, {name: "beta"}}
	slices.SortFunc(items, func(a, b *Item) int {
		return cmp.Compare(a.name, b.name)
	})

	fmt.Printf("%d,%d,%d,%s,%s,%s\n", c1, c2, c3, items[0].name, items[1].name, items[2].name)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "s279_string_order.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, stderr.String())
	}
	const want = "-1,1,0,alpha,beta,gamma"
	got := strings.TrimSpace(stdout.String())
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashPPS279VerifiedPlanFastPath verifies that repeated imported calls
// capture and reuse s.verifiedPlan on the native session under s.mu.
func TestBashPPS279VerifiedPlanFastPath(t *testing.T) {
	const source = `package main
import "strings"

func main() {
	total := 0
	for i := 0; i < 20; i++ {
		if strings.Contains("space-time", "time") {
			total++
		}
	}
	println(total)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "s279_plan.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	var runner *Runner
	var session *bashPPNativeSession
	var verifiedPlan *bashPPNativeRequestPlan
	probe := callbackProbeWriter(func(p []byte) (int, error) {
		stderr.Write(p)
		if strings.Contains(string(p), "20") && runner != nil {
			session = runner.bashPPTools.bridge
			if session != nil {
				session.mu.Lock()
				verifiedPlan = session.verifiedPlan
				session.mu.Unlock()
			}
		}
		return len(p), nil
	})
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &stdout, probe))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "20" {
		t.Fatalf("output = %q, want 20", got)
	}
	if session == nil {
		t.Fatal("session not captured")
	}
	if verifiedPlan == nil {
		t.Fatal("expected non-nil verifiedPlan on native session while active")
	}
}
