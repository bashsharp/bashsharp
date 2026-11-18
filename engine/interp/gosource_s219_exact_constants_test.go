//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS219ExactConstants(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{
			"rational chain",
			`package main
import "fmt"
const ( a = 3.0/2; b = a/3; c = b*2 )
func main(){ fmt.Println(a, b, c, c == 1) }
`,
			"1.5 0.5 1 true\n",
		},
		{
			"large integer rational cancellation",
			`package main
import "fmt"
const huge = 1 << 240
const ratio = (1.0 * (huge + 1)) / huge
const cancelled = (ratio - 1) * huge
func main(){ fmt.Println(cancelled, cancelled == 1) }
`,
			"1 true\n",
		},
		{
			"typed and untyped",
			`package main
import "fmt"
type Measure float64
const u = 3.0/2
const typed Measure = u
const again = typed / 3
func main(){ fmt.Println(u, typed, again, again*2 == 1) }
`,
			"1.5 1.5 0.5 true\n",
		},
		{
			"forward dependency",
			`package main
import "fmt"
const answer = numerator / denominator
const denominator = 6
const numerator = denominator * 7
func main(){ fmt.Println(answer) }
`,
			"7\n",
		},
		{
			"runtime float rounding unchanged",
			`package main
import "fmt"
const exact = 1.0/10
func main(){ x := float64(exact); x = x + 0.2; fmt.Printf("%.17g\n", x) }
`,
			"0.30000000000000004\n",
		},
		{
			"imports precede constant preparation",
			`package main
import ( "fmt"; "math"; "time" )
const second = time.Second
const circle = math.Pi
func main(){ fmt.Println(second, circle > 3) }
`,
			"1s true\n",
		},
		{
			"group forward reference",
			`package main
import "fmt"
const ( a = b + 1; b = 2 )
func main(){ fmt.Println(a, b) }
`,
			"3 2\n",
		},
		{
			"group dependencies do not falsely cycle",
			`package main
import "fmt"
const ( a = b + 1; b = c + 1; c = 1 )
func main(){ fmt.Println(a, b, c) }
`,
			"3 2 1\n",
		},
		{
			"iota and inherited expression",
			`package main
import "fmt"
const ( a = iota + 10; b; c )
func main(){ fmt.Println(a, b, c) }
`,
			"10 11 12\n",
		},
		{
			"typed group rounding and exact rational",
			`package main
import "fmt"
const ( x float32 = 16777217; y = x - 16777216; third = 1.0/3; restored = third*3 )
func main(){ fmt.Println(x, y, restored, restored == 1) }
`,
			"1.6777216e+07 0 1 true\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "s219exact", tc.source)
			if err != nil || stderr != "" || out != tc.want {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
		})
	}

	t.Run("cycle rejected", func(t *testing.T) {
		_, err := gosource.Parse(strings.NewReader(`package main
const a = b + 1
const b = a + 1
func main(){}
`), "s219cycle.go", gosource.Options{RunMain: true})
		if err == nil || !strings.Contains(err.Error(), "initialization cycle") {
			t.Fatalf("err=%v", err)
		}
	})
}
