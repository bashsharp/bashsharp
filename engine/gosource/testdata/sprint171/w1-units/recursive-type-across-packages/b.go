package b

import "example.com/rec/a"

// T embeds a.T: a legal cross-package embedding that a flat unit turns into
// a type referring to itself.
type T struct{ a.T }

func (T) m() { println("ok") }

func F1(i interface{ m() }) { i.m() }

func F2(i interface {
	m()
	a.I
}) {
	i.m()
}

// V is a variable named like the type a.V; U embeds the type, not the
// variable.
var V struct{ i int }

var U struct {
	a.V
	j int
}
