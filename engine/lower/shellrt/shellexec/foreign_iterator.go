package shellexec

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func NewForeignIterator(module *polyglot.Module, name string, args []any) *shellrt.ForeignIterator {
	signature, _ := module.StreamSignature(name)
	return &shellrt.ForeignIterator{
		Element: signature.Iterator,
		Decode: func(ctx context.Context, line, element string) (any, string, string, error) {
			frame, err := module.DecodeIteratorFrame(ctx, line, element)
			return frame.Value, frame.Stdout, frame.Stderr, err
		},
		Start: func(ctx context.Context) (shellrt.IteratorProcess, error) {
			return interp.StartForeignIterator(ctx, module, name, args, shellrt.Stdout, shellrt.Stderr)
		},
	}
}

func NewPythonHandleIterator(handle *polyglot.Handle) *shellrt.ForeignIterator {
	return &shellrt.ForeignIterator{
		Element: "any",
		DomainError: func(err error) bool {
			_, ok := polyglot.ForeignErrorDetail(err)
			return ok
		},
		Decode: func(ctx context.Context, line, element string) (any, string, string, error) {
			frame, err := handle.DecodeIteratorFrame(ctx, line, element)
			return frame.Value, frame.Stdout, frame.Stderr, err
		},
		Start: func(ctx context.Context) (shellrt.IteratorProcess, error) {
			return interp.StartForeignHandleIterator(ctx, handle, shellrt.Stdout, shellrt.Stderr)
		},
	}
}
