package interp

import (
	"fmt"
	"strings"
	"sync"
)

// Sprint: #376; Story: #1550; Story-ID: cae490ea17e7
//
// Route slots: how a callback finds the routed request that raised it.
//
// A routed request's callbacks name it (routedCallbackRequest), and the only
// link between the two in the helper is the goroutine dispatching the
// request. The helper used to read that goroutine's number out of a
// runtime.Stack capture on every callback. The capture formats the whole
// stack under the runtime's single print lock, so parallel callers — a
// hundred goroutines each calling a reflect.MakeFunc function — ran one at a
// time through it, and it cost more than the callback's own work.
//
// A routed dispatch now runs beneath one of a fixed set of functions that
// differ only in their code address, and records its request in that
// function's slot. A callback walks its own return addresses
// (runtime.Callers, which takes no lock and prints nothing) to the nearest
// such function and reads the slot. A slot is written by the goroutine that
// claimed it and read only by code running beneath its frame, which is that
// same goroutine. When every slot is taken, or the frame is further up than
// the walk reaches, the goroutine-number table still answers.
const bashPPRouteSlots = 256

var bashPPRouteSlotWorkerSource = sync.OnceValue(func() string {
	var b strings.Builder
	fmt.Fprintf(&b, "const bppRouteSlotCount=%d\n", bashPPRouteSlots)
	for i := range bashPPRouteSlots {
		fmt.Fprintf(&b, "//go:noinline\nfunc bppRouteSlot%d(f func()){f()}\n", i)
	}
	b.WriteString("var bppRouteSlotFuncs=[bppRouteSlotCount]func(func()){")
	for i := range bashPPRouteSlots {
		fmt.Fprintf(&b, "bppRouteSlot%d,", i)
	}
	b.WriteString("}\n")
	return b.String()
})
