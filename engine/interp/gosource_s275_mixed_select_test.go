//go:build full

package interp_test

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5

import (
	"strings"
	"testing"
)

// A select mixing a live dependency channel (a cancelable context's Done, a
// time.After timer) with interpreter-owned channels is arbitrated so exactly
// one arm proceeds. Each case pins one readiness shape to what Go does.
func TestS275MixedNativeSelect(t *testing.T) {
	const prelude = `package main

import (
	"context"
	"fmt"
	"time"
)

var _, _ = time.Millisecond, context.Background

`
	for _, tc := range []struct{ name, body, want string }{
		{"native_ready", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	values := make(chan int)
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v := <-values:
		fmt.Println("value", v)
	}
}`, "canceled\n"},
		{"local_send_ready", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int, 1)
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case values <- 7:
		fmt.Println("sent", <-values)
	}
}`, "sent 7\n"},
		{"default_neither_ready", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int)
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v := <-values:
		fmt.Println("value", v)
	default:
		fmt.Println("default")
	}
}`, "default\n"},
		{"default_local_ready", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int, 1)
	values <- 3
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v := <-values:
		fmt.Println("value", v)
	default:
		fmt.Println("default")
	}
}`, "value 3\n"},
		{"default_native_ready", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	values := make(chan int)
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v := <-values:
		fmt.Println("value", v)
	default:
		fmt.Println("default")
	}
}`, "canceled\n"},
		{"blocking_wakes_on_native", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	values := make(chan int)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	select {
	case <-ctx.Done():
		fmt.Println("canceled", ctx.Err())
	case v := <-values:
		fmt.Println("value", v)
	}
}`, "canceled context canceled\n"},
		{"blocking_wakes_on_timer", `func main() {
	values := make(chan int)
	select {
	case <-time.After(20 * time.Millisecond):
		fmt.Println("timeout")
	case v := <-values:
		fmt.Println("value", v)
	}
}`, "timeout\n"},
		{"blocking_wakes_on_local", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int)
	go func() {
		time.Sleep(20 * time.Millisecond)
		values <- 9
	}()
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v := <-values:
		fmt.Println("value", v)
	}
}`, "value 9\n"},
		{"blocking_observes_close", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int)
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(values)
	}()
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case v, ok := <-values:
		fmt.Println("value", v, ok)
	}
}`, "value 0 false\n"},
		{"no_loss_no_duplication", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	values := make(chan int)
	done := make(chan int)
	go func() {
		sent := 0
		for i := 1; i <= 200; i++ {
			select {
			case <-ctx.Done():
				done <- sent
				return
			case values <- i:
				sent += i
			}
		}
		done <- sent
	}()
	got := 0
	for i := 0; i < 150; i++ {
		got += <-values
	}
	cancel()
	fmt.Println(got == <-done, got)
}`, "true 11325\n"},
		{"both_ready_either_wins", `func main() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	values := make(chan int, 1)
	native, local, total := 0, 0, 0
	for i := 1; i <= 200; i++ {
		values <- i
		select {
		case <-ctx.Done():
			native++
			total += <-values
		case v := <-values:
			local++
			total += v
		}
	}
	fmt.Println(native > 0, local > 0, native+local, total)
}`, "true true 200 20100\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "s275-"+tc.name, prelude+tc.body+"\n")
			if err != nil || stderr != "" || out != tc.want {
				t.Fatalf("run=%v stdout=%q stderr=%q want %q", err, out, stderr, tc.want)
			}
		})
	}
}

// A send arm on a closed interpreter-owned channel is ready, and choosing it
// panics exactly as the runtime does, even beside a live dependency arm.
func TestS275MixedNativeSelectClosedSend(t *testing.T) {
	const source = `package main

import "context"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan int)
	close(values)
	select {
	case <-ctx.Done():
	case values <- 1:
	}
}
`
	_, stderr, err := runGoSource(t, "s275-closed-send", source)
	if err == nil || !strings.Contains(stderr, "send on closed channel") {
		t.Fatalf("run=%v stderr=%q", err, stderr)
	}
}
