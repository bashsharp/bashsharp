//go:build full

package lower_test

import "testing"

// These are the unchanged successful sources from
// interp/bashpp_directional_test.go:TestBashPPDirectionalChannelRuntime.
// The oracle is checked first. execute builds actual Compile output, removes
// generated source, then checks native stdout/stderr/status against the oracle.
// In particular, a closed int channel's empty source rendering is intentional.
func TestDirectionalChannelAcceptance(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"send and receive", "func send(ch chan<- int) {\n ch <- 7\n}\nfunc recv(ch <-chan int) {\n value := <-ch\n println(value)\n}\nfunc main() {\nch := make(chan int, 1)\nsend(ch)\nrecv(ch)\n}\nmain()\n", "7\n"},
		{"close send only", "func finish(ch chan<- int) {\n close(ch)\n}\nfunc recv(ch <-chan int) {\n v, ok := <-ch\n println(v, ok)\n}\nfunc main() {\nch := make(chan int, 1)\nfinish(ch)\nrecv(ch)\n}\nmain()\n", " false\n"},
		{"directed return", "func narrow(ch chan int) <-chan int {\n return ch\n}\nfunc recv(ch <-chan int) {\n v := <-ch\n println(v)\n}\nfunc main() {\nch := make(chan int, 1)\nch <- 9\nread := narrow(ch)\nrecv(read)\n}\nmain()\n", "9\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, status := genericMethodOracle(t, tc.source)
			t.Logf("source stdout=%q stderr=%q status=%d", out, stderr, status)
			if out != tc.want || stderr != "" || status != 0 {
				t.Fatalf("public directional source contract changed")
			}
			execute(t, compile(t, tc.source))
		})
	}
}
