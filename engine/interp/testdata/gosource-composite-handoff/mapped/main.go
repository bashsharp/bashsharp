package main

import "handoff/a"

// The same spelling in another package must not satisfy this interface.
type reader[T comparable] interface{ read() T }

func main() {
	value := a.Value()
	if _, ok := value.(reader[string]); ok {
		panic("private method package lost")
	}
}
