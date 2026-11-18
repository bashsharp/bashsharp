package shellrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// IteratorProcess is implemented by the single B14 line-process substrate.
type IteratorProcess interface {
	Lines() <-chan string
	Wait() (int, error)
	Close() error
	Stderr() string
}

type ForeignIterator struct {
	mu      sync.Mutex
	Start   func(context.Context) (IteratorProcess, error)
	Element string
	// Decode is supplied by the shell backend. Keep these runtime declarations
	// independent of foreign-worker packages so standalone transpilation never
	// requires the user project to resolve the engine module.
	Decode func(context.Context, string, string) (value any, stdout, stderr string, err error)
	// DomainError identifies a worker-raised iteration error which an explicit
	// second range binding may receive. Infrastructure failures never qualify.
	DomainError func(error) bool
	used        bool
}

// IteratorSequence owns the producer for exactly this range. A break invokes
// Close before returning, so generated code never leaves an infinite producer.
func IteratorSequence(ctx context.Context, value any) func(func(any) bool) {
	return func(yield func(any) bool) {
		iterator, ok := value.(*ForeignIterator)
		if !ok {
			panic(ValueAbort{Err: fmt.Errorf("%T is not a foreign iterator", value)})
		}
		iterator.mu.Lock()
		used := iterator.used
		iterator.used = true
		iterator.mu.Unlock()
		if used {
			return
		}
		process, err := iterator.Start(ctx)
		if err != nil {
			panic(ValueAbort{Err: err})
		}
		defer func() {
			err := process.Close()
			_, _ = io.WriteString(Stderr, process.Stderr())
			if err != nil && !errors.Is(err, context.Canceled) {
				panic(ValueAbort{Err: err})
			}
		}()
		for line := range process.Lines() {
			value, stdout, stderr, err := iterator.Decode(ctx, line, iterator.Element)
			if err != nil {
				panic(ValueAbort{Err: err})
			}
			fmt.Fprint(Stdout, stdout)
			fmt.Fprint(Stderr, stderr)
			if !yield(value) {
				return
			}
		}
		status, err := process.Wait()
		if err != nil {
			panic(ValueAbort{Err: err})
		}
		if status != 0 {
			Status = status
			panic(ValueAbort{Err: fmt.Errorf("foreign iterator exited with status %d", status)})
		}
	}
}

// IteratorErrorSequence is the two-binding Python-handle range. Normal EOF
// yields nothing; a worker domain error yields exactly one final nil,error
// pair. Close remains deferred around that final yield, including break.
func IteratorErrorSequence(ctx context.Context, value any) func(func(any, error) bool) {
	return func(yield func(any, error) bool) {
		iterator, ok := value.(*ForeignIterator)
		if !ok {
			panic(ValueAbort{Err: fmt.Errorf("%T is not a foreign iterator", value)})
		}
		iterator.mu.Lock()
		used := iterator.used
		iterator.used = true
		iterator.mu.Unlock()
		if used {
			return
		}
		process, err := iterator.Start(ctx)
		if err != nil {
			if iterator.DomainError != nil && iterator.DomainError(err) {
				yield(nil, TrustedErrorText(err.Error()))
				return
			}
			panic(ValueAbort{Err: err})
		}
		handledDomain := false
		defer func() {
			err := process.Close()
			_, _ = io.WriteString(Stderr, process.Stderr())
			if handledDomain && iterator.DomainError != nil && iterator.DomainError(err) {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				panic(ValueAbort{Err: err})
			}
		}()
		for line := range process.Lines() {
			item, stdout, stderr, err := iterator.Decode(ctx, line, iterator.Element)
			if err != nil {
				panic(ValueAbort{Err: err})
			}
			fmt.Fprint(Stdout, stdout)
			fmt.Fprint(Stderr, stderr)
			if !yield(item, nil) {
				return
			}
		}
		status, err := process.Wait()
		if err != nil {
			if iterator.DomainError != nil && iterator.DomainError(err) {
				handledDomain = true
				yield(nil, TrustedErrorText(err.Error()))
				return
			}
			panic(ValueAbort{Err: err})
		}
		if status != 0 {
			Status = status
			panic(ValueAbort{Err: fmt.Errorf("foreign iterator exited with status %d", status)})
		}
	}
}
