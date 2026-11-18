package interp_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
	"time"
)

func TestBashPPDirectionalChannelRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, functions, body, wantOut, wantErr string
		status                                  uint8
	}{
		{"send and receive", "func send(ch chan<- int) {\n ch <- 7\n}\nfunc recv(ch <-chan int) {\n value := <-ch\n println(value)\n}\n", "ch := make(chan int, 1)\nsend(ch)\nrecv(ch)", "7\n", "", 0},
		{"close send only", "func finish(ch chan<- int) {\n close(ch)\n}\nfunc recv(ch <-chan int) {\n v, ok := <-ch\n println(v, ok)\n}\n", "ch := make(chan int, 1)\nfinish(ch)\nrecv(ch)", " false\n", "", 0},
		{"reject receive only send", "func f(ch <-chan int) {\n ch <- 1\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot send on recv-only channel ch\n", 2},
		{"reject send only receive", "func f(ch chan<- int) {\n v := <-ch\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot receive on send-only channel ch\n", 2},
		{"reject receive only close", "func f(ch <-chan int) {\n close(ch)\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot close on recv-only channel ch\n", 2},
		{"reject widening", "func unrestricted(ch chan int) {\n echo unreachable\n}\nfunc f(ch <-chan int) {\n unrestricted(ch)\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-EARG-CHAN: unrestricted requires chan int for parameter ch\n", 2},
		{"reject forged handle", "func f(ch <-chan int) {\n echo unreachable\n}\n", "ch := make(chan int, 1)\nforged := \"$ch\"\nf(forged)", "", "BASHPP-EARG-CHAN: f requires <-chan int for parameter ch\n", 2},
		{"directed return", "func narrow(ch chan int) <-chan int {\n return ch\n}\nfunc recv(ch <-chan int) {\n v := <-ch\n println(v)\n}\n", "ch := make(chan int, 1)\nch <- 9\nread := narrow(ch)\nrecv(read)", "9\n", "", 0},
		{"reject alias receive", "func f(ch chan<- int) {\n alias := ch\n v := <-alias\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot receive on send-only channel alias\n", 2},
		{"reject select receive", "func f(ch chan<- int) {\n select {\n case v := <-ch:\n echo unreachable\n default:\n echo unreachable\n }\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot receive on send-only channel ch\n", 2},
		{"reject range receive", "func f(ch chan<- int) {\n for v := range ch {\n echo unreachable\n }\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-ECHAN-DIRECTION: cannot receive on send-only channel ch\n", 2},
		{"reject return widening", "func widen(ch <-chan int) chan int {\n return ch\n}\n", "ch := make(chan int, 1)\nwide := widen(ch)", "", "BASHPP-ERETURN-CHAN: widen requires chan int result\n", 2},
		{"reject wrong element", "func f(ch <-chan string) {\n echo unreachable\n}\n", "ch := make(chan int, 1)\nf(ch)", "", "BASHPP-EARG-CHAN: f requires <-chan string for parameter ch\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.functions + "func main() {\n" + tc.body + "\n}\nmain()\n"
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "direction.bpp")
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = runner.Run(ctx, file)
			var status uint8
			if err != nil {
				var code interp.ExitStatus
				if !errors.As(err, &code) {
					t.Fatal(err)
				}
				status = uint8(code)
			}
			if ctx.Err() != nil {
				t.Fatal("channel operation leaked a blocking wait")
			}
			if out.String() != tc.wantOut || stderr.String() != tc.wantErr || status != tc.status {
				t.Fatalf("out=%q stderr=%q status=%d want %q %q %d", out.String(), stderr.String(), status, tc.wantOut, tc.wantErr, tc.status)
			}
		})
	}
}
