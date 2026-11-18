package shellrt

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

func TestNativeContinuationKeepsCapturesButNotAuthority(t *testing.T) {
	continuation := NewNativeContinuation()
	first, err := NewProgram(WithNativeContinuation(continuation))
	if err != nil {
		t.Fatal(err)
	}
	channel, err := MakeChannel[int](first.Channels, 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := first.Run(func(p *Program) {
		p.DefineNativeShell("value", false, func(*Program) { calls++ })
		p.DefineNativeShell("marked", true, func(*Program) { t.Error("continued marker bypassed") })
		p.DefineNativeShell("channel", false, func(p *Program) { MustChannelOperation(Send(p.Context, p.Session, p.Channels, channel, 1)) })
	}); err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	second, err := NewProgram(WithNativeContinuation(continuation), WithStdio(nil, nil, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	if second.Channels == first.Channels || second.Bindings == first.Bindings {
		t.Fatal("continued entry reused native root")
	}
	err = second.Run(func(p *Program) {
		if !p.CallNativeShell(Site{Name: "value"}) {
			t.Error("lost continued native definition")
		}
		p.CallNativeShell(Site{Name: "marked"})
		p.CallNativeShell(Site{Name: "channel"})
	})
	if calls != 1 || !errors.Is(err, ErrForeignChannel) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	if diagnostic.String() != "marked: agentic action requires an explicit agentic { ...; } scope\n" {
		t.Fatalf("permission diagnostic %q", diagnostic.String())
	}
}

func TestNativeContinuationSerializesCapturedWrites(t *testing.T) {
	continuation := NewNativeContinuation()
	owner, err := NewProgram(WithNativeContinuation(continuation))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := owner.Run(func(p *Program) { p.DefineNativeShell("increment", false, func(*Program) { count++ }) }); err != nil {
		t.Fatal(err)
	}
	const entries = 24
	var workers sync.WaitGroup
	failures := make(chan error, entries)
	for i := 0; i < entries; i++ {
		workers.Go(func() {
			p, err := NewProgram(WithNativeContinuation(continuation))
			if err == nil {
				err = p.Run(func(p *Program) { p.CallNativeShell(Site{Name: "increment"}) })
			}
			failures <- err
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != entries {
		t.Fatalf("captured count %d", count)
	}
}

func TestNativeContinuationWaitHonorsCancellation(t *testing.T) {
	continuation := NewNativeContinuation()
	entered, release := make(chan struct{}), make(chan struct{})
	owner, err := NewProgram(WithNativeContinuation(continuation))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owner.Run(func(*Program) { close(entered); <-release }) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiting, err := NewProgram(WithNativeContinuation(continuation), WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	err = waiting.Run(func(*Program) { t.Error("canceled continuation executed") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
