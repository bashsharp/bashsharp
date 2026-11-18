package polyglot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Iterator owns an iterator in the existing persistent worker. Each request
// advances it once, so backpressure never requires buffering its entire result.
// Closing drops only the iterator, preserving the module and its other handles.
type Iterator struct {
	module     *Module
	id         any
	generation uint64
	mu         sync.Mutex
	closed     bool
	callbacks  []uint64
}

func (m *Module) OpenIterator(ctx context.Context, name string, args []any) (*Iterator, CallResult, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return nil, CallResult{}, err
	}
	defer unlock()
	if err := m.ensure(ctx); err != nil {
		return nil, CallResult{}, err
	}
	defer func() { m.dropPendingCallbacks()() }()
	if languageFlag(m.plan.Language, func(language Language) bool { return language.Callbacks }) {
		args, err = m.resolveCallbacks(name, args)
		if err != nil {
			return nil, CallResult{}, err
		}
	}
	encoded, err := m.encodeValue(args)
	if err != nil {
		return nil, CallResult{}, err
	}
	m.nextID++
	result, err := m.request(ctx, map[string]any{"id": m.nextID, "op": "iter_open", "name": name, "args": encoded}, nil)
	if err != nil {
		return nil, result, err
	}
	ids := append([]uint64(nil), m.pendingCallbacks...)
	m.pendingCallbacks = nil
	return &Iterator{module: m, id: result.Value, generation: m.generation, callbacks: ids}, result, nil
}

// OpenIterator adapts this authenticated Python value to the shared foreign
// iterator lifecycle. The handle's module and worker generation are checked
// before its ID is sent to the worker; descriptive handle metadata never
// grants iterator authority.
func (h *Handle) OpenIterator(ctx context.Context) (*Iterator, CallResult, error) {
	if h == nil || h.module == nil {
		return nil, CallResult{}, h.stale()
	}
	return h.module.OpenHandleIterator(ctx, h)
}

// OpenHandleIterator is the module-explicit form used by hosts which retain
// worker ownership separately from the opaque handle. Cross-module handles
// are refused even when their numeric worker IDs happen to match.
func (m *Module) OpenHandleIterator(ctx context.Context, h *Handle) (*Iterator, CallResult, error) {
	if _, ok := m.runtime.(Python); !ok || h == nil || h.module != m {
		return nil, CallResult{}, fmt.Errorf("polyglot: stale or foreign %s handle", m.runtime.name())
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return nil, CallResult{}, err
	}
	defer unlock()
	if err := m.ensure(ctx); err != nil {
		return nil, CallResult{}, err
	}
	if h.generation != m.generation {
		return nil, CallResult{}, fmt.Errorf("polyglot: stale or foreign %s handle", m.runtime.name())
	}
	m.nextID++
	result, err := m.request(ctx, map[string]any{"id": m.nextID, "op": "iter_open", "handle": h.ID}, nil)
	if err != nil {
		return nil, result, err
	}
	return &Iterator{module: m, id: result.Value, generation: m.generation}, result, nil
}

func (it *Iterator) request(ctx context.Context, op string) (CallResult, error) {
	m := it.module
	unlock, err := m.lock(ctx)
	if err != nil {
		return CallResult{}, err
	}
	defer unlock()
	if op == "iter_close" {
		defer func() {
			for _, id := range it.callbacks {
				delete(m.callbackTable, id)
			}
			it.callbacks = nil
		}()
	}
	if it.generation != m.generation || m.in == nil {
		return CallResult{}, errors.New("stale foreign iterator: worker restarted")
	}
	m.nextID++
	result, err := m.request(ctx, map[string]any{"id": m.nextID, "op": op, "iterator": it.id}, nil)

	return result, err
}

func (it *Iterator) Next(ctx context.Context) (CallResult, bool, error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.closed {
		return CallResult{}, false, nil
	}
	result, err := it.request(ctx, "iter_next")
	if err != nil {
		return result, false, err
	}
	frame, ok := result.Value.(map[string]any)
	if !ok {
		return result, false, errors.New("invalid iterator frame")
	}
	done, _ := frame["done"].(bool)
	result.Value = frame["value"]
	if done {
		// Close also releases host callback authority after EOF.
		closed, closeErr := it.request(ctx, "iter_close")
		result.Stdout += closed.Stdout
		result.Stderr += closed.Stderr
		if closeErr != nil {
			return result, false, closeErr
		}
		it.closed = true
	}
	return result, !done, nil
}

func (it *Iterator) Close(ctx context.Context) (CallResult, error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.closed {
		return CallResult{}, nil
	}
	it.closed = true
	return it.request(ctx, "iter_close")
}

// PipelineInput is the distinct pull-based pipeline capability. It shares the
// callback registry and reverse frames, but never decodes as a callable proxy.
type PipelineInput struct{ Callback Callback }

// FilterInput adapts a byte reader to a PipelineInput.
func FilterInput(reader io.Reader) PipelineInput {
	return PipelineInput{Callback: Callback{Name: "pipeline stdin", Invoke: func(ctx context.Context, args []any) (any, error) {
		size := int64(65536)
		if len(args) > 0 {
			if n, ok := args[0].(int64); ok && n > 0 && n < size {
				size = n
			}
		}
		data := make([]byte, size)
		type readResult struct {
			n   int
			err error
		}
		done := make(chan readResult, 1)
		go func() { n, err := reader.Read(data); done <- readResult{n, err} }()
		var n int
		var err error
		select {
		case result := <-done:
			n, err = result.n, result.err
		case <-ctx.Done():
			if closer, ok := reader.(io.Closer); ok {
				closer.Close()
				<-done
			}
			return nil, ctx.Err()
		}
		if err == io.EOF {
			err = nil
		}
		return data[:n], err
	}}}
}

// EncodeIteratorFrame and DecodeIteratorFrame retain the ordinary Object codec
// across the B14 line substrate, including nested bytes and worker-owned handles.
func (m *Module) EncodeIteratorFrame(ctx context.Context, result CallResult) ([]byte, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	encoded, err := m.encodeValue(result.Value)
	if err != nil {
		return nil, err
	}
	encoded = iteratorHandleMetadata(result.Value, encoded)
	return json.Marshal(map[string]any{"value": encoded, "stdout": result.Stdout, "stderr": result.Stderr, "generation": m.generation})
}
func (m *Module) DecodeIteratorFrame(ctx context.Context, line, element string) (CallResult, error) {
	var frame struct {
		Generation     uint64
		Value          any
		Stdout, Stderr string
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&frame); err != nil {
		return CallResult{}, err
	}
	// Decoding is local and must not queue behind a next request blocked on
	// pipeline input. Only worker identity is read; the process mutex protects
	// it while cancellation/restart may occur concurrently.
	if err := ctx.Err(); err != nil {
		return CallResult{}, err
	}
	m.procMu.Lock()
	defer m.procMu.Unlock()
	if frame.Generation != m.generation {
		return CallResult{}, errors.New("stale iterator frame: worker restarted")
	}
	value, err := m.decodeValue(frame.Value)
	if err == nil && element != "" {
		value, err = coerceResult(value, element)
	}
	return CallResult{Value: value, Stdout: frame.Stdout, Stderr: frame.Stderr}, err
}

// Iterator frame helpers keep lowered handle ranges on the same authenticated
// module codec without exposing the owning Module as handle metadata.
func (h *Handle) EncodeIteratorFrame(ctx context.Context, result CallResult) ([]byte, error) {
	if h == nil || h.module == nil {
		return nil, h.stale()
	}
	return h.module.EncodeIteratorFrame(ctx, result)
}

func (h *Handle) DecodeIteratorFrame(ctx context.Context, line, element string) (CallResult, error) {
	if h == nil || h.module == nil {
		return CallResult{}, h.stale()
	}
	return h.module.DecodeIteratorFrame(ctx, line, element)
}

// Preserve the worker's descriptive handle envelope while the ordinary encoder
// validates module/generation ownership. Request encoding intentionally uses
// only the ID; stream output carries the same metadata as worker output.
func iteratorHandleMetadata(original, encoded any) any {
	switch value := original.(type) {
	case *Handle:
		return map[string]any{"$handle": value}
	case []any:
		out := encoded.([]any)
		for i, item := range value {
			out[i] = iteratorHandleMetadata(item, out[i])
		}
	case map[string]any:
		out := encoded.(map[string]any)
		for key, item := range value {
			out[key] = iteratorHandleMetadata(item, out[key])
		}
	}
	return encoded
}
