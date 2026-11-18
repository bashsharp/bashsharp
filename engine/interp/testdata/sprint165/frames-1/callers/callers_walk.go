package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// A stack walk sees the interpreted frames by their Go names and, below
// main.main, the runtime's own runtime.main and runtime.goexit; runtime.Callers
// lists itself first and fills return counters that resolve at pc-1 as well as
// at pc; runtime.CallersFrames replays them as runtime.Frame values with a
// working Func. A deferred call running for a panic is called by
// runtime.gopanic, which tracebacks print as panic({...}). The positive
// control is the walk with no panic in flight; the negatives are a skip past
// the last frame (ok is false, nothing named) and a counter no function owns.

var skip int

type frame struct {
	name string
	line int
	ok   bool
}

var got frame

func h() {
	pc, _, line, ok := runtime.Caller(skip)
	got = frame{"", line, ok}
	if ok {
		got.name = runtime.FuncForPC(pc).Name()
	}
}

func g() {
	h()
}

func f() {
	g()
}

// caller walks f→g→h with runtime.Caller(skip) and reports the frame.
func caller(skp int) frame {
	skip = skp
	f()
	return got
}

var npcs int
var pcs = make([]uintptr, 32)

func callers() {
	npcs = runtime.Callers(skip, pcs)
}

// names walks the counters runtime.Callers filled, at pc-1 as Go documents.
func names(skp int) []string {
	skip = skp
	callers()
	var out []string
	for i := 0; i < npcs; i++ {
		fn := runtime.FuncForPC(pcs[i] - 1)
		out = append(out, fn.Name())
		if fn.Name() == "main.main" {
			break
		}
	}
	return out
}

// frames replays the counters through runtime.CallersFrames.
func frames(skp int) []string {
	skip = skp
	callers()
	ci := runtime.CallersFrames(pcs[:npcs])
	var out []string
	for {
		fr, more := ci.Next()
		if strings.HasPrefix(fr.Function, "runtime.") {
			out = append(out, fr.Function)
		} else {
			out = append(out, fmt.Sprintf("%s@%d", fr.Function, fr.Line))
		}
		if fr.Func.Name() != fr.Function || fr.Entry != fr.Func.Entry() || fr.PC < fr.Entry {
			out = append(out, "frame disagrees with its Func")
		}
		if !more || fr.Function == "main.main" {
			break
		}
	}
	return out
}

type T struct{}

func (T) M() string {
	return func() string {
		pc, _, _, _ := runtime.Caller(0)
		return runtime.FuncForPC(pc).Name()
	}()
}

func gen[X any](x X) string {
	pc, _, _, _ := runtime.Caller(0)
	return runtime.FuncForPC(pc).Name()
}

func fault() {
	panic("boom")
}

// unwind reports the walk a deferred call sees while its panic is live.
func unwind() {
	defer func() {
		recover()
		var chain []string
		for i := 0; i < 5; i++ {
			pc, _, _, ok := runtime.Caller(i)
			if !ok {
				break
			}
			chain = append(chain, runtime.FuncForPC(pc).Name())
		}
		fmt.Println("chain during unwind:", strings.Join(chain, " "))
		trace := string(debug.Stack())
		fmt.Println("trace starts with debug.Stack:", strings.HasPrefix(trace, "goroutine 1 [running]:\nruntime/debug.Stack()\n"))
		fmt.Println("trace has panic frame:", strings.Contains(trace, "\npanic({"))
		fmt.Println("trace has fault:", strings.Contains(trace, "main.fault()\n"))
		var buf [4096]byte
		n := runtime.Stack(buf[:], false)
		fmt.Println("runtime.Stack starts with the caller:", strings.HasPrefix(string(buf[:n]), "goroutine 1 [running]:\nmain.unwind.func1()\n"))
	}()
	fault()
}

func main() {
	for i := 0; i <= 7; i++ {
		fr := caller(i)
		if strings.HasPrefix(fr.name, "runtime.") {
			fmt.Printf("Caller(%d): %s ok=%v\n", i, fr.name, fr.ok)
			continue
		}
		fmt.Printf("Caller(%d): %s line=%d ok=%v\n", i, fr.name, fr.line, fr.ok)
	}
	fmt.Println("Callers(0):", strings.Join(names(0), " "))
	fmt.Println("Callers(1):", strings.Join(names(1), " "))
	fmt.Println("Callers(3):", strings.Join(names(3), " "))
	fmt.Println("Frames(0):", strings.Join(frames(0), " "))
	fmt.Println("Frames(2):", strings.Join(frames(2), " "))
	empty := runtime.CallersFrames(nil)
	fr, more := empty.Next()
	fmt.Println("no counters:", fr.Function == "", fr.Line, more)
	fmt.Println("unknown counter:", runtime.FuncForPC(0) == nil)
	fmt.Println("literals:", T{}.M(), gen(1), func() string {
		return func() string {
			pc, _, _, _ := runtime.Caller(0)
			return runtime.FuncForPC(pc).Name()
		}()
	}())
	unwind()
	fmt.Println("after unwind:", strings.Join(names(1), " "))
}
