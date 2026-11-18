package interp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// A worker iterator is a process source, not a second channel supervisor. The
// B14 producer alone owns bounded delivery and close. The source translates
// pull responses from the persistent child to frames and closes that one
// iterator on abandonment; the module retains ownership of the child itself.
type bashPPIteratorSource struct {
	reader         *io.PipeReader
	writer         *io.PipeWriter
	cancel         context.CancelFunc
	done           chan struct{}
	stop           chan struct{}
	once           sync.Once
	output, stderr io.Writer
	final          polyglot.CallResult
	finalErr       error
}

func (s *bashPPIteratorSource) Stdout() io.Reader { return s.reader }
func (s *bashPPIteratorSource) Stderr() io.Reader { return nil }
func (s *bashPPIteratorSource) Wait() error {
	<-s.done
	if s.output != nil {
		fmt.Fprint(s.output, s.final.Stdout)
	}
	if s.stderr != nil {
		fmt.Fprint(s.stderr, s.final.Stderr)
	}
	return s.finalErr
}
func (s *bashPPIteratorSource) Kill() {
	s.once.Do(func() {
		close(s.stop)
		s.reader.Close()
		go func() {
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-s.done:
			case <-timer.C:
				s.cancel()
			}
		}()
	})
}

// StartForeignIterator shares the live-process bounded channel substrate.
func StartForeignIterator(ctx context.Context, module *polyglot.Module, name string, args []any, output, stderr io.Writer) (*bashPPLineProcess, error) {
	worker, initial, err := module.OpenIterator(ctx, name, args)
	return startForeignIterator(ctx, worker, initial, err, module.EncodeIteratorFrame, output, stderr)
}

// StartForeignHandleIterator adapts an authenticated returned Python handle
// to the same bounded line-process used by signature-declared iterators.
func StartForeignHandleIterator(ctx context.Context, handle *polyglot.Handle, output, stderr io.Writer) (*bashPPLineProcess, error) {
	worker, initial, err := handle.OpenIterator(ctx)
	return startForeignIterator(ctx, worker, initial, err, handle.EncodeIteratorFrame, output, stderr)
}

func startForeignIterator(ctx context.Context, worker *polyglot.Iterator, initial polyglot.CallResult, openErr error, encode func(context.Context, polyglot.CallResult) ([]byte, error), output, stderr io.Writer) (*bashPPLineProcess, error) {
	if output != nil {
		fmt.Fprint(output, initial.Stdout)
	}
	if stderr != nil {
		fmt.Fprint(stderr, initial.Stderr)
	}
	if openErr != nil {
		return nil, openErr
	}
	read, write := io.Pipe()
	sourceCtx, cancel := context.WithCancel(ctx)
	src := &bashPPIteratorSource{reader: read, writer: write, cancel: cancel, done: make(chan struct{}), stop: make(chan struct{}), output: output, stderr: stderr}
	go func() {
		defer close(src.done)
		defer cancel()
		defer func() {
			closeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer stop()
			closed, closeErr := worker.Close(closeCtx)
			src.final.Stdout += closed.Stdout
			src.final.Stderr += closed.Stderr
			if sourceCtx.Err() == nil {
				src.finalErr = closeErr
			}
		}()
		for {
			select {
			case <-src.stop:
				write.Close()
				return
			default:
			}
			result, more, err := worker.Next(sourceCtx)

			if err != nil {
				src.final.Stdout += result.Stdout
				src.final.Stderr += result.Stderr
				write.CloseWithError(err)
				return
			}
			if !more {
				src.final.Stdout += result.Stdout
				src.final.Stderr += result.Stderr
				write.Close()
				return
			}
			frame, err := encode(sourceCtx, result)
			if err == nil {
				_, err = fmt.Fprintf(write, "%s\n", frame)
			}
			if err != nil {
				write.CloseWithError(err)
				return
			}
		}
	}()
	return bashPPStartLineProcess(ctx, src, bashPPDefaultLineBuffer), nil
}
