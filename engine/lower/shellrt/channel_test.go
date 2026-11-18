package shellrt

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func channelSession(t *testing.T) *Session {
	t.Helper()
	session, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func TestNativeChannelValuesAndClose(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	defer scope.Close()
	channel, err := MakeChannel[string](scope, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Send(context.Background(), session, scope, channel, "ready"); err != nil {
		t.Fatal(err)
	}
	if err := CloseChannel(scope, channel); err != nil {
		t.Fatal(err)
	}
	value, ok, err := Receive(context.Background(), session, scope, channel)
	if err != nil || !ok || value != "ready" {
		t.Fatalf("%q %v %v", value, ok, err)
	}
	value, ok, err = Receive(context.Background(), session, scope, channel)
	if err != nil || ok || value != "" {
		t.Fatalf("%q %v %v", value, ok, err)
	}
	if err := Send(context.Background(), session, scope, channel, "late"); err == nil || !strings.Contains(err.Error(), "send on closed") {
		t.Fatal(err)
	}
	if err := CloseChannel(scope, channel); err == nil {
		t.Fatal("double close accepted")
	}
	if _, err := MakeChannel[int](scope, 65537); err == nil {
		t.Fatal("capacity cap ignored")
	}
}
func TestOwnedBlockingChannelsCancelAndArm(t *testing.T) {
	for _, operation := range []string{"send", "receive", "empty-select"} {
		t.Run(operation, func(t *testing.T) {
			session := channelSession(t)
			scope := &ChannelScope{}
			defer scope.Close()
			channel := MustChannel(MakeChannel[int](scope, 0))
			launched := make(chan *Task, 1)
			go func() {
				launched <- session.Go(ChannelTask(func(ctx context.Context, child *Session) error {
					switch operation {
					case "send":
						return Send(ctx, child, scope, channel, 1)
					case "receive":
						_, _, err := Receive(ctx, child, scope, channel)
						return err
					default:
						_, err := SelectChannels(ctx, child, scope, nil)
						return err
					}
				}))
			}()
			var task *Task
			select {
			case task = <-launched:
			case <-time.After(time.Second):
				t.Fatal("blocking operation failed to arm launch")
			}
			session.Cancel()
			done := make(chan error, 1)
			go func() { done <- task.Wait() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation leaked child")
			}
		})
	}
}
func TestCloseUnregistersBlockedSends(t *testing.T) {
	for _, useSelect := range []bool{false, true} {
		session := channelSession(t)
		scope := &ChannelScope{}
		channel := MustChannel(MakeChannel[int](scope, 0))
		task := session.Go(func(ctx context.Context, child *Session) error {
			if useSelect {
				_, err := SelectChannels(ctx, child, scope, []ChannelCase{SendCase(scope, channel, 1)})
				return err
			}
			return Send(ctx, child, scope, channel, 1)
		})
		if err := CloseChannel(scope, channel); err != nil {
			t.Fatal(err)
		}
		if err := task.Wait(); err == nil || !strings.Contains(err.Error(), "send on closed") {
			t.Fatal(err)
		}
		scope.Close()
	}
}
func TestSelectNativeValuesAndDefault(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	defer scope.Close()
	channel := MustChannel(MakeChannel[int](scope, 1))
	if err := Send(context.Background(), session, scope, channel, 7); err != nil {
		t.Fatal(err)
	}
	selected, err := SelectChannels(context.Background(), session, scope, []ChannelCase{ReceiveCase(scope, channel), DefaultCase()})
	if err != nil || selected.Index != 0 {
		t.Fatalf("%+v %v", selected, err)
	}
	value, ok := SelectedReceive(selected, channel)
	if value != 7 || !ok {
		t.Fatalf("%d %v", value, ok)
	}
	selected, err = SelectChannels(context.Background(), session, scope, []ChannelCase{ReceiveCase(scope, channel), DefaultCase()})
	if err != nil || selected.Index != 1 {
		t.Fatalf("%+v %v", selected, err)
	}
}
func TestChannelScopeRevocationAndForeignIdentity(t *testing.T) {
	session := channelSession(t)
	owner, foreign := &ChannelScope{}, &ChannelScope{}
	channel := MustChannel(MakeChannel[int](owner, 0))
	if err := Send(context.Background(), session, foreign, channel, 1); !errors.Is(err, ErrForeignChannel) {
		t.Fatal(err)
	}
	task := session.Go(func(ctx context.Context, child *Session) error {
		_, _, err := Receive(ctx, child, owner, channel)
		return err
	})
	owner.Close()
	if err := task.Wait(); !errors.Is(err, ErrChannelScopeClosed) {
		t.Fatal(err)
	}
	if _, err := MakeChannel[int](owner, 0); !errors.Is(err, ErrChannelScopeClosed) {
		t.Fatal(err)
	}
}
func TestCanceledOperationsDoNotPublish(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	channel := MustChannel(MakeChannel[int](scope, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Send(ctx, session, scope, channel, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(channel) != 0 {
		t.Fatal("pre-canceled send published a value")
	}
}
func TestChannelConcurrentCloseSendRace(t *testing.T) {
	for i := 0; i < 30; i++ {
		session := channelSession(t)
		scope := &ChannelScope{}
		channel := MustChannel(MakeChannel[int](scope, 0))
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); _ = Send(context.Background(), session, scope, channel, 1) }()
		go func() { defer wait.Done(); _ = CloseChannel(scope, channel) }()
		wait.Wait()
		scope.Close()
	}
}

func TestSelectClosedSendDoesNotSuppressReadyReceive(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	closed := MustChannel(MakeChannel[int](scope, 0))
	ready := MustChannel(MakeChannel[int](scope, 1))
	if err := CloseChannel(scope, closed); err != nil {
		t.Fatal(err)
	}
	// Many independent selections verify the closed send remains one ready case
	// among peers, rather than becoming an unconditional preflight error.
	received := false
	for i := 0; i < 100; i++ {
		if len(ready) == 0 {
			if err := Send(context.Background(), session, scope, ready, 1); err != nil {
				t.Fatal(err)
			}
		}
		selected, err := SelectChannels(context.Background(), session, scope, []ChannelCase{SendCase(scope, closed, 1), ReceiveCase(scope, ready)})
		if err == nil && selected.Index == 1 {
			received = true
			break
		}
	}
	if !received {
		t.Fatal("closed send suppressed every ready receive")
	}
}

func TestChannelCapabilitiesRejectStringsAndRevoke(t *testing.T) {
	scope := &ChannelScope{}
	channel := MustChannel(MakeChannel[int](scope, 1))
	reference, err := ChannelReference(scope, channel)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := ResolveChannel[int](scope, reference); err != nil || resolved != channel {
		t.Fatal(resolved, err)
	}
	for _, forged := range []any{reference.String(), "__bashpp_channel:1", &ChannelCapability{}} {
		if _, err := ResolveChannel[int](scope, forged); err == nil {
			t.Fatalf("forged authority accepted: %T", forged)
		}
	}
	if _, err := ResolveChannel[string](scope, reference); err == nil {
		t.Fatal("wrong element type accepted")
	}
	if _, err := ResolveChannel[int](&ChannelScope{}, reference); err == nil {
		t.Fatal("foreign scope accepted")
	}
	if err := CheckChannelBoundary(map[string]any{"nested": []any{reference}}); err == nil {
		t.Fatal("nested capability escaped boundary")
	}
	if err := CheckChannelBoundary(channel); err == nil {
		t.Fatal("native channel escaped boundary")
	}
	if err := CheckChannelBoundary(reference.String()); err != nil {
		t.Fatal("authority-free display text rejected", err)
	}
	scope.Close()
	if _, err := ResolveChannel[int](scope, reference); !errors.Is(err, ErrChannelScopeClosed) {
		t.Fatal(err)
	}
}

func TestSelectClosedInterfaceChannelZero(t *testing.T) {
	session := channelSession(t)
	scope := &ChannelScope{}
	channel := MustChannel(MakeChannel[any](scope, 0))
	if err := CloseChannel(scope, channel); err != nil {
		t.Fatal(err)
	}
	selection, err := SelectChannels(context.Background(), session, scope, []ChannelCase{ReceiveCase(scope, channel)})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := SelectedReceive(selection, channel)
	if value != nil || ok {
		t.Fatalf("%v %v", value, ok)
	}
}
