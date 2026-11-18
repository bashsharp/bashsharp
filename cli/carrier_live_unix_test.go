//go:build unix

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qiangli/bash/interp"
)

func TestCarrierLiveSignalEvents(t *testing.T) {
	cp, err := (execJobCarrier{}).StartCarrierWithIgnoredSignals(context.Background(), []string{"INT", "QUIT"})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Terminate()
	live, ok := cp.(interp.SignalAwareCarrierProcess)
	if !ok {
		t.Fatal("carrier has no live signal protocol")
	}
	next := func() interp.CarrierSignalState {
		t.Helper()
		done := make(chan interp.CarrierSignalState, 1)
		go func() { done <- live.WaitSignalState() }()
		select {
		case state := <-done:
			return state
		case <-time.After(3 * time.Second):
			cp.Terminate()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("carrier waiter did not finish after termination")
			}
			t.Fatal("carrier state did not arrive")
			return interp.CarrierSignalState{}
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGINT, syscall.SIGQUIT} {
		if err := syscall.Kill(cp.Pid(), sig); err != nil {
			t.Fatal(err)
		}
		state := next()
		if !state.Delivered || state.Signal != int(sig) || state.Stopped {
			t.Fatalf("signal event: %+v", state)
		}
		if err := syscall.Kill(cp.Pid(), 0); err != nil {
			t.Fatalf("caught delivery lost carrier: %v", err)
		}
	}
	// Distinct deliveries must both survive without a per-send ACK.
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGQUIT} {
		if err := syscall.Kill(cp.Pid(), sig); err != nil {
			t.Fatal(err)
		}
	}
	pending := map[int]bool{int(syscall.SIGINT): true, int(syscall.SIGQUIT): true}
	for len(pending) > 0 {
		state := next()
		if !state.Delivered || !pending[state.Signal] {
			t.Fatalf("unexpected queued signal state: %+v", state)
		}
		delete(pending, state.Signal)
	}
	if err := syscall.Kill(cp.Pid(), syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if state := next(); state.Delivered || !state.Stopped {
		t.Fatalf("stop became signal event: %+v", state)
	}
	cp.Terminate()
	if state := next(); state.Delivered || state.Stopped {
		t.Fatalf("termination not terminal: %+v", state)
	}
	concrete := cp.(*execCarrierProc)
	if concrete.signals != nil {
		t.Fatal("terminal state did not drain signal reader")
	}
	if _, err := concrete.events.Stat(); err == nil {
		t.Fatal("event descriptor still open after terminal state")
	}
	waitPidsGone(t, []int{cp.Pid()})
}

func TestCarrierLiveJobDispositions(t *testing.T) {
	for _, compound := range []string{"parentheses", "braces"} {
		for _, sender := range []string{"builtin", "external"} {
			t.Run(compound+"/"+sender, func(t *testing.T) {
				open, close := "(", ")"
				if compound == "braces" {
					open, close = "{", "}"
				}
				kill := "kill"
				if sender == "external" {
					kill = "/bin/kill"
					if _, err := os.Stat(kill); err != nil {
						kill = "/usr/bin/kill"
					}
				}
				source := fmt.Sprintf(`SUT=%q
%s
 count=0
 trap 'count=$((count+1)); printf "CAUGHT:%%s\n" "$count"' INT
 trap - QUIT
 (trap 'printf "INNER_WRONG\n"' INT)
 identity=$("$SUT" -c 'printf "%%s" "$PPID"')
 kill -s 0 "$identity" || exit 90
 %s -s INT "$identity"
 while [ "$count" -lt 1 ]; do :; done
 kill -0 "$identity" || exit 91
 %s -s INT "$identity"
 while [ "$count" -lt 2 ]; do :; done
 kill -0 "$identity" || exit 92
 %s -s QUIT "$identity"
 while :; do :; done
%s &
identity=$!
wait "$identity"
printf 'STATUS=%%s\n' "$?"
if kill -0 "$identity" 2>/dev/null; then echo LEAK; fi
`, builtBashBin(t), open, kill, kill, kill, close)
				out, status := runBuiltBash(t, t.TempDir(), source)
				if status != 0 || out != "CAUGHT:1\nCAUGHT:2\nSTATUS=131\n" {
					t.Fatalf("status=%d output=%q", status, out)
				}
			})
		}
	}
}

func TestCarrierLiveSignalsPreserveDefaultIgnore(t *testing.T) {
	source := fmt.Sprintf(`SUT=%q
(
 identity=$("$SUT" -c 'printf "%%s" "$PPID"')
 /bin/kill -s INT "$identity"
 /bin/kill -s QUIT "$identity"
 kill -0 "$identity" || exit 91
 printf 'SURVIVED\n'
) &
wait "$!"
printf 'STATUS=%%s\n' "$?"
`, builtBashBin(t))
	out, status := runBuiltBash(t, t.TempDir(), source)
	if status != 0 || strings.TrimSpace(out) != "SURVIVED\nSTATUS=0" {
		t.Fatalf("status=%d output=%q", status, out)
	}
}

func TestCarrierLiveLegacyWait(t *testing.T) {
	cp, err := (execJobCarrier{}).StartCarrierWithIgnoredSignals(context.Background(), []string{"INT"})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Terminate()
	done := make(chan int, 1)
	go func() { done <- cp.Wait() }()
	if err := syscall.Kill(cp.Pid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(cp.Pid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-done:
		if sig != int(syscall.SIGTERM) {
			t.Fatalf("legacy terminal signal=%d", sig)
		}
	case <-time.After(3 * time.Second):
		cp.Terminate()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("legacy waiter did not finish")
		}
		t.Fatal("legacy Wait failed to terminate")
	}
	waitPidsGone(t, []int{cp.Pid()})
}

func TestCarrierLiveBuiltinKillCannotBeTrapped(t *testing.T) {
	source := fmt.Sprintf(`SUT=%q
(
 trap 'echo WRONG_TRAP' KILL
 identity=$("$SUT" -c 'printf "%%s" "$PPID"')
 kill -KILL "$identity"
 echo WRONG_AFTER
) &
wait "$!"
printf 'STATUS=%%s\n' "$?"
`, builtBashBin(t))
	out, status := runBuiltBash(t, t.TempDir(), source)
	if status != 0 || strings.TrimSpace(out) != "STATUS=137" {
		t.Fatalf("status=%d output=%q", status, out)
	}
}
