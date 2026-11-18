//go:build full

package interp_test

// Sprint: #243; Story: #672; Story-ID: fa5b3cf5a929

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS243ScalarOriginalRoots(t *testing.T) {
	for _, name := range []string{"const.go", "nil.go", "typeparam/issue51522b.go"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", name))
			if err != nil {
				t.Fatal(err)
			}
			typedSendThreeModes(t, string(source))
		})
	}
}

func TestS243ScalarConversionsThreeModes(t *testing.T) {
	cases := map[string]string{
		"exact_constants_assign_to_scalars": `package main

import "fmt"

const (
	huge = 1 << 100
	typed float64 = huge - 1
	fraction = 3.0 / 2.0
)

func main() {
	var f float64
	f = typed
	f = fraction
	fmt.Println(f, typed == float64(huge), fraction == 1.5)
}
`,
		"context_converts_named_scalar_constant": `package main

import (
	"fmt"
	"time"
)

func duration(d time.Duration) time.Duration { return d }

func main() {
	fmt.Println(duration(1e7) == 10*time.Millisecond)
	time.Sleep(0.0)
}
`,
		"switch_interface_and_concrete_scalar": `package main

type myint int
func (myint) foo() {}
type fooer interface { foo() }
type comparableFoo interface { comparable; foo() }

func check[T comparableFoo](i fooer) {
	var value T
	switch i { case value: println("i") }
	switch value { case i: println("value") }
}

func main() { check[myint](myint(0)) }
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestS243NamedScalarConstantOverflowRejected(t *testing.T) {
	const source = `package main

import "time"

func main() { time.Sleep(1e100) }
`
	_, err := gosource.Parse(strings.NewReader(source), "overflow.go", gosource.Options{RunMain: true})
	if err == nil || (!strings.Contains(err.Error(), "overflows") &&
		!(strings.Contains(err.Error(), "cannot use") && strings.Contains(err.Error(), "time.Duration"))) {
		t.Fatalf("expected checked constant overflow, got %v", err)
	}
}
