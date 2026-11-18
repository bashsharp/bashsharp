package shellrt_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func TestTryValueGuardErrors(t *testing.T) {
	site := shellrt.ValueSite{File: "binding.bpp", Name: "p", Line: 3, Column: 7}
	valueError := &shellrt.ValueError{Code: "BASHPP-ENIL-DEREF", Message: "dereference of nil pointer", Site: site}
	readonlyError := &shellrt.ReadonlyError{Message: "cannot assign to readonly value \"x\""}
	for _, failure := range []error{valueError, shellrt.ValueAbort{Err: valueError}, readonlyError} {
		t.Run(fmt.Sprintf("%T", failure), func(t *testing.T) {
			calls, deferred := 0, 0
			value, err := shellrt.TryValue(func() int {
				calls++
				defer func() { deferred++ }()
				panic(failure)
			})
			if value != 0 || err != failure || calls != 1 || deferred != 1 {
				t.Fatalf("value=%d err=%v calls=%d deferred=%d", value, err, calls, deferred)
			}
			if err.(interface{ ExitStatus() int }).ExitStatus() != 2 {
				t.Fatal("lost checked error status")
			}
			if failure != readonlyError {
				var got *shellrt.ValueError
				if !errors.As(err, &got) || got != valueError || got.Site != site {
					t.Fatal("lost positioned error identity")
				}
			}
		})
	}
}

func TestTryValuePreservesControlTransfers(t *testing.T) {
	userError := &shellrt.ExitError{Status: 2}
	valueError := &shellrt.ValueError{Code: "test", Message: "wrapped source payload"}
	for _, payload := range []any{
		"source panic", userError, fmt.Errorf("source wrapper: %w", valueError),
		shellrt.ChannelAbort{Err: context.Canceled}, shellrt.ShellExit{},
	} {
		t.Run(fmt.Sprintf("%T", payload), func(t *testing.T) {
			var recovered any
			deferred, continued := false, false
			func() {
				defer func() { recovered = recover() }()
				_, _ = shellrt.TryValue(func() int {
					defer func() { deferred = true }()
					panic(payload)
				})
				continued = true
			}()
			if recovered != payload || !deferred || continued {
				t.Fatalf("recovered=%#v deferred=%v continued=%v", recovered, deferred, continued)
			}
		})
	}
}

func TestTryValuePreservesSourceRecoverFrame(t *testing.T) {
	value, err := shellrt.TryValue(func() (result string) {
		defer func() { result = shellrt.PreserveAbort(recover()).(string) }()
		panic("caught")
	})
	if value != "caught" || err != nil {
		t.Fatalf("direct recover: value=%q err=%v", value, err)
	}
	indirect := func() any { return recover() }
	var direct any
	func() {
		defer func() { direct = recover() }()
		_, _ = shellrt.TryValue(func() int {
			defer func() {
				if indirect() != nil {
					t.Error("indirect recover consumed panic")
				}
			}()
			panic("uncaught")
		})
	}()
	if direct != "uncaught" {
		t.Fatalf("outer direct recover=%v", direct)
	}
	_, err = shellrt.TryValue(func() int {
		defer func() { shellrt.PreserveAbort(recover()) }()
		return shellrt.MustValue(shellrt.Deref[int](nil, shellrt.ValueSite{}))
	})
	if err == nil {
		t.Fatal("source recover consumed checked failure")
	}
}

func TestTryValueBindingCommitAndIdentity(t *testing.T) {
	type pair struct{ A, B int }
	existing := pair{7, 9}
	effects := 0
	candidate, err := shellrt.TryValue(func() pair {
		effects++
		return pair{11, shellrt.MustValue(shellrt.Deref[int](nil, shellrt.ValueSite{}))}
	})
	if err == nil {
		existing = candidate
	}
	if err == nil || candidate != (pair{}) || existing != (pair{7, 9}) || effects != 1 {
		t.Fatalf("candidate=%v existing=%v effects=%d err=%v", candidate, existing, effects, err)
	}
	var pointer *int
	value, err := shellrt.TryValue(func() *int { return pointer })
	if err != nil || shellrt.BindingValue(true, value, "p") != pointer {
		t.Fatal("typed nil became an absent binding")
	}
	number := 4
	alias := shellrt.BindingValue(true, &number, "p").(*int)
	*alias = 8
	if number != 8 || shellrt.BindingValue(false, &number, "p") != "p" || shellrt.BindingValue(false, 0, "") != "" {
		t.Fatal("binding identity or lexical fallback changed")
	}
}

// This exercises the generated caller contract with real Programs. Full source
// compilation and interpreter parity belong to the compiler acceptance tests.
func TestTryValueProgramContinuation(t *testing.T) {
	var wg sync.WaitGroup
	for range 8 {
		p, out, diagnostics := newProgram(t)
		wg.Go(func() {
			err := p.Run(func(p *shellrt.Program) {
				value, err := shellrt.TryValue(func() int {
					return shellrt.MustValue(shellrt.Deref[int](nil, shellrt.ValueSite{Name: "p"}))
				})
				if err != nil {
					p.Fail(err)
					// The generated caller must retain this failure across
					// subsequent output, which updates the command status.
					defer p.SetStatus(err.(interface{ ExitStatus() int }).ExitStatus())
				}
				p.Println(shellrt.BindingValue(err == nil, value, "x"))
			})
			if err != nil || p.Status() != 2 || out.String() != "x\n" || diagnostics.String() != "BASHPP-ENIL-DEREF: dereference of nil pointer\n" {
				t.Errorf("err=%v status=%d stdout=%q stderr=%q", err, p.Status(), out.String(), diagnostics.String())
			}
		})
	}
	wg.Wait()
}
