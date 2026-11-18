package shellrt

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A deadline is only a deadlock watchdog; task arming establishes the test's
// prepared-send barrier. Every worker has an explicit completion observation.
func reviewAwait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("channel operation or cleanup remained blocked")
		return nil
	}
}
func reviewAsync(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

func TestChannelReviewSelectReleasesEverySend(t *testing.T) {
	for _, outcome := range []string{"default", "receive", "send", "canceled", "closed-peer", "invalid-scope"} {
		t.Run(outcome, func(t *testing.T) {
			session := channelSession(t)
			scope := &ChannelScope{}
			defer scope.Close()
			a := MustChannel(MakeChannel[int](scope, 0))
			b := MustChannel(MakeChannel[string](scope, 0))
			cases := []ChannelCase{SendCase(scope, a, 1), SendCase(scope, a, 2), SendCase(scope, b, "three")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := MustChannel(MakeChannel[int](scope, 1))
			switch outcome {
			case "default":
				cases = append(cases, DefaultCase())
			case "receive":
				ready <- 9
				cases = append(cases, ReceiveCase(scope, ready))
			case "send":
				cases = append(cases, SendCase(scope, ready, 9))
			case "closed-peer":
				if err := CloseChannel(scope, ready); err != nil {
					t.Fatal(err)
				}
				cases = append(cases, SendCase(scope, ready, 9))
			case "invalid-scope":
				cases = append(cases, SendCase(&ChannelScope{}, ready, 9))
			}
			var selected ChannelSelection
			var err error
			if outcome == "canceled" {
				task := session.Go(func(_ context.Context, child *Session) error {
					_, err := SelectChannels(ctx, child, scope, cases)
					return err
				})
				cancel()
				err = reviewAwait(t, reviewAsync(task.Wait))
			} else {
				selected, err = SelectChannels(ctx, session, scope, cases)
			}
			switch outcome {
			case "default", "receive", "send":
				if err != nil || selected.Index != 3 {
					t.Fatalf("selection=%+v error=%v", selected, err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "closed-peer":
				if err == nil || !strings.Contains(err.Error(), "send on closed") {
					t.Fatal(err)
				}
			case "invalid-scope":
				if !errors.Is(err, ErrForeignChannel) {
					t.Fatal(err)
				}
			}
			// A leaked registration on any unselected or duplicate send deadlocks close.
			for _, closeFn := range []func() error{func() error { return CloseChannel(scope, a) }, func() error { return CloseChannel(scope, b) }} {
				if err := reviewAwait(t, reviewAsync(closeFn)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestChannelReviewClosedSendAndOperandCapture(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	defer scope.Close()
	closed := MustChannel(MakeChannel[int](scope, 0))
	if err := CloseChannel(scope, closed); err != nil {
		t.Fatal(err)
	}
	blocked := MustChannel(MakeChannel[int](scope, 0))
	var order []string
	operand := func(label string, ch chan int) chan int { order = append(order, label); return ch }
	value := func() int { order = append(order, "value"); return 17 }
	cases := []ChannelCase{SendCase(scope, operand("send", closed), value()), ReceiveCase(scope, operand("receive", blocked)), DefaultCase()}
	if strings.Join(order, ",") != "send,value,receive" {
		t.Fatal(order)
	}
	for i := 0; i < 32; i++ {
		selected, err := SelectChannels(context.Background(), session, scope, cases)
		if err == nil || !strings.Contains(err.Error(), "send on closed") {
			t.Fatalf("closed send must beat default: %+v %v", selected, err)
		}
	}
	if len(order) != 3 {
		t.Fatal("selection reevaluated operands", order)
	}
	// A case captures the value at construction, even if the source cell changes.
	ready := MustChannel(MakeChannel[int](scope, 1))
	n := 23
	captured := SendCase(scope, ready, n)
	n = 99
	if _, err := SelectChannels(context.Background(), session, scope, []ChannelCase{captured}); err != nil {
		t.Fatal(err)
	}
	if got := <-ready; got != 23 {
		t.Fatalf("captured value=%d, mutated cell=%d", got, n)
	}
}

func TestChannelReviewCloseCancelAndRevoke(t *testing.T) {
	for round := 0; round < 24; round++ {
		session, err := NewSession()
		if err != nil {
			t.Fatal(err)
		}
		scope := &ChannelScope{}
		a := MustChannel(MakeChannel[int](scope, 0))
		b := MustChannel(MakeChannel[int](scope, 0))
		receiveOnly := MustChannel(MakeChannel[int](scope, 0))
		cap, err := ChannelReference(scope, a)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		tasks := []*Task{
			session.Go(func(_ context.Context, s *Session) error { return Send(ctx, s, scope, a, 1) }),
			session.Go(func(_ context.Context, s *Session) error {
				_, _, err := Receive(ctx, s, scope, receiveOnly)
				return err
			}),
			session.Go(func(_ context.Context, s *Session) error {
				_, err := SelectChannels(ctx, s, scope, []ChannelCase{SendCase(scope, a, 2), SendCase(scope, a, 3), SendCase(scope, b, 4)})
				return err
			}),
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(4)
		actions := []func(){func() {
			for i := 0; i < 4; i++ {
				cancel()
				session.Cancel()
			}
		}, func() { _ = CloseChannel(scope, a); _ = CloseChannel(scope, b) }, func() { scope.Close(); scope.Close() }, func() { _ = session.Close() }}
		for _, action := range actions {
			go func(fn func()) { defer wg.Done(); <-start; fn() }(action)
		}
		close(start)
		reviewAwait(t, reviewAsync(func() error { wg.Wait(); return nil }))
		for _, task := range tasks {
			err := reviewAwait(t, reviewAsync(task.Wait))
			if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, ErrChannelScopeClosed) && !strings.Contains(err.Error(), "send on closed")) {
				t.Fatalf("round %d: task outcome %v", round, err)
			}
		}
		reviewAwait(t, reviewAsync(session.Close))
		if session.Active() != 0 {
			t.Fatal("owner retained active task")
		}
		if _, err := ResolveChannel[int](scope, cap); !errors.Is(err, ErrChannelScopeClosed) {
			t.Fatal("capability survived revocation", err)
		}
		if err := Send(context.Background(), nil, scope, a, 9); !errors.Is(err, ErrChannelScopeClosed) {
			t.Fatal("revoked scope admitted send", err)
		}
	}
}

func TestChannelReviewCloseSendHandshake(t *testing.T) {
	for _, operation := range []string{"send", "duplicate-select"} {
		t.Run(operation, func(t *testing.T) {
			session := channelSession(t)
			scope := &ChannelScope{}
			defer scope.Close()
			ch := MustChannel(MakeChannel[int](scope, 0))
			tasks := make([]*Task, 12)
			for i := range tasks {
				tasks[i] = session.Go(func(ctx context.Context, s *Session) error {
					if operation == "send" {
						return Send(ctx, s, scope, ch, 1)
					}
					_, err := SelectChannels(ctx, s, scope, []ChannelCase{SendCase(scope, ch, 1), SendCase(scope, ch, 2)})
					return err
				})
			}
			if err := reviewAwait(t, reviewAsync(func() error { return CloseChannel(scope, ch) })); err != nil {
				t.Fatal(err)
			}
			// Close returned only after every registered send stopped touching the
			// native channel, so a direct receive must now report its closed zero.
			select {
			case v, ok := <-ch:
				if ok || v != 0 {
					t.Fatalf("close published %d %v", v, ok)
				}
			default:
				t.Fatal("close returned before native close")
			}
			for _, task := range tasks {
				err := reviewAwait(t, reviewAsync(task.Wait))
				if err == nil {
					t.Fatal("blocked send succeeded without a receiver")
				}
			}
			if err := CloseChannel(scope, ch); err == nil || !strings.Contains(err.Error(), "close of closed") {
				t.Fatal(err)
			}
		})
	}
}

// The public interpreter requirement TestBashPPImmediateChannelFailureExcludesLaterTask
// forbids an immediate ready/default outcome from arming the next launch. A
// scheduler yield after that nonblocking outcome makes the admission race
// repeatable without inserting another blocking runtime operation.
func TestChannelReviewImmediateFailureDoesNotArmLaterLaunch(t *testing.T) {
	for _, operation := range []string{"ready-send", "ready-receive", "closed-receive", "ready-select", "default-select", "closed-select"} {
		t.Run(operation, func(t *testing.T) {
			session := channelSession(t)
			scope := &ChannelScope{}
			defer scope.Close()
			ch := MustChannel(MakeChannel[int](scope, 1))
			if operation == "closed-select" || operation == "closed-receive" {
				if err := CloseChannel(scope, ch); err != nil {
					t.Fatal(err)
				}
			} else if operation != "default-select" && operation != "ready-send" {
				ch <- 1
			}
			failure := errors.New("immediate body failure")
			first := session.Go(func(ctx context.Context, s *Session) error {
				var err error
				switch operation {
				case "ready-send":
					err = Send(ctx, s, scope, ch, 1)
				case "ready-receive", "closed-receive":
					_, _, err = Receive(ctx, s, scope, ch)
				case "ready-select":
					_, err = SelectChannels(ctx, s, scope, []ChannelCase{ReceiveCase(scope, ch)})
				case "default-select":
					_, err = SelectChannels(ctx, s, scope, []ChannelCase{ReceiveCase(scope, ch), DefaultCase()})
				case "closed-select":
					_, err = SelectChannels(ctx, s, scope, []ChannelCase{SendCase(scope, ch, 1)})
				}
				runtime.Gosched()
				if err != nil {
					return err
				}
				return failure
			})
			var escaped atomic.Bool
			later := session.Go(func(context.Context, *Session) error { escaped.Store(true); return nil })
			if err := reviewAwait(t, reviewAsync(first.Wait)); err == nil {
				t.Fatal("first task did not fail")
			}
			reviewAwait(t, reviewAsync(later.Wait))
			if escaped.Load() {
				t.Fatalf("%s armed later launch before immediate failure", operation)
			}
		})
	}
}
