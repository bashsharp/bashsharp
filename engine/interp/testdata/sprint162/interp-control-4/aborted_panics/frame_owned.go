package main

import "fmt"

// A deferred call sees only the panic its deferring frame is unwinding:
// once it has recovered the nested panic that interrupted an outer one, a
// second recover in the same call yields nil — the outer panic belongs to
// the outer frame's own deferred calls, which recover it in turn. `v =
// recover()` binds the interface value like `v := recover()`, and a
// deferred recover that has nothing to take is not a failed cleanup.
func mustRecover(x interface{}) {
	v := recover()
	fmt.Println("mustRecover:", v, "want", x)
	v = recover()
	fmt.Println("second recover:", v)
}

func nested() {
	defer mustRecover(1)
	defer func() {
		defer mustRecover(2)
		panic(2)
	}()
	panic(1)
}

func swapped() {
	defer mustRecover(2)
	defer func() {
		defer mustRecover(3)
		defer recover() // a no-op: called from the panic, not from the func
		panic(3)
	}()
	panic(2)
}

func main() {
	nested()
	swapped()
	fmt.Println("done")
}
