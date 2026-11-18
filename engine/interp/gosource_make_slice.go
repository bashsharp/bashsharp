// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"go/constant"
	"runtime"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sizes have already been evaluated in source order. An integer that cannot
// fit the host int is an invalid dynamic size, not an argument type error.
func (r *Runner) goSourceMakeSliceSize(arg bashPPBuiltinArg) (int, bool) {
	if r.bashPPGoSource && arg.hasScalar && arg.scalar.value != nil && arg.scalar.value.Kind() == constant.Int {
		n, ok := constant.Int64Val(arg.scalar.value)
		if !ok || int64(int(n)) != n {
			return -1, true
		}
		return int(n), true
	}
	return r.bashPPBuiltinInt("make", arg)
}

// Match runtime/malloc.go's heapAddrBits/maxAlloc for the running target;
// this is an address-space bound, not a resource budget for the interpreter.
func goSourceMaxAllocation() uint64 {
	bits := uint(48)
	if strconv.IntSize == 32 || runtime.GOARCH == "wasm" {
		bits = 32
	}
	if runtime.GOARCH == "mips" || runtime.GOARCH == "mipsle" {
		bits = 31
	}
	if runtime.GOOS == "ios" && runtime.GOARCH == "arm64" {
		bits = 40
	}
	bound := uint64(1) << bits
	if strconv.IntSize == 32 {
		bound--
	}
	return bound
}

// Test source element sizes, never the larger []any carrier slot. Division
// avoids multiplication overflow. A zero-size element imposes no byte bound.
// Preserve the compiler's negative-length and len>cap checks, then the
// runtime's length-before-capacity priority for allocation byte overflow.
func (r *Runner) goSourceMakeSliceFault(element syntax.BashPPTypeExpr, length, capacity int) bool {
	if !r.bashPPGoSource {
		return false
	}
	if length < 0 {
		return r.bashPPSprint162MakeSlicePanic(-1, -1)
	}
	if capacity < length {
		return r.bashPPSprint162MakeSlicePanic(0, -1)
	}
	layout, ok := r.goSourceLayoutType(element, make(map[string]bool))
	if !ok {
		layout = r.bashPPEmbeddedNativeType(element)
	}
	if layout == nil {
		r.exit.fatal(fmt.Errorf("gosource: cannot determine slice element allocation layout"))
		return true
	}
	elementSize := bashPPGoSizes().Sizeof(layout)
	if elementSize < 0 {
		r.exit.fatal(fmt.Errorf("gosource: slice element allocation layout exceeds target size"))
		return true
	}
	size := uint64(elementSize)
	invalid := func(n int) bool { return size != 0 && uint64(n) > goSourceMaxAllocation()/size }
	if invalid(length) {
		return r.bashPPSprint162MakeSlicePanic(-1, -1)
	}
	if invalid(capacity) {
		return r.bashPPSprint162MakeSlicePanic(0, -1)
	}
	return false
}

// Catch only allocation-size runtime faults, at the carrier allocation itself.
// Source sizes were checked already. A valid source allocation that exceeds
// dense interpreter storage is an explicit representation limit, not a false
// Go runtime.Error (notably for very large zero-size-element slices).
func goSourceAllocateSlice(length, capacity int) (values []any, children []*bashPPCollectionMeta, err error) {
	defer func() {
		if fault := recover(); fault != nil {
			if runtimeError, ok := fault.(runtime.Error); ok && (runtimeError.Error() == "runtime error: makeslice: len out of range" || runtimeError.Error() == "runtime error: makeslice: cap out of range") {
				values, children = nil, nil
				err = fmt.Errorf("gosource: slice size exceeds interpreter carrier capacity")
				return
			}
			panic(fault)
		}
	}()
	return make([]any, length, capacity), make([]*bashPPCollectionMeta, length, capacity), nil
}

func (r *Runner) goSourceAllocateTypedSlice(element syntax.BashPPTypeExpr, length, capacity int) (values []any, children []*bashPPCollectionMeta, logical bool, err error) {
	shape, ok := r.bashPPGoShapeType(element, 0)
	if !ok || shape.Size() != 0 {
		values, children, err = goSourceAllocateSlice(length, capacity)
		return values, children, false, err
	}
	values, children, err = goSourceAllocateSlice(length, capacity)
	if err == nil {
		return values, children, false, nil
	}
	return nil, nil, true, nil
}
