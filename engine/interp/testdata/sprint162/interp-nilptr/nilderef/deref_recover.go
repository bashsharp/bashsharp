package main

import "fmt"

type T struct{ val int }

func field(p *T) int { return p.val }

func deref(p *int) int { return *p }

func compare(p *int) bool { return *p >= 0 }

func guard(name string, f func()) {
	defer func() {
		r := recover()
		fmt.Println(name, "recovered:", r)
	}()
	f()
	fmt.Println(name, "did not panic")
}

func main() {
	var t *T
	var p *int
	guard("field", func() { fmt.Println(field(t)) })
	guard("deref", func() { fmt.Println(deref(p)) })
	guard("compare", func() { fmt.Println(compare(p)) })
	guard("switch", func() {
		switch t.val {
		case 0:
			fmt.Println("zero")
		}
	})
	guard("multi", func() {
		var bad bool
		bad, _ = true, *p
		fmt.Println(bad)
	})
	guard("element", func() {
		var m [2]*int
		fmt.Println(*m[1])
	})
	guard("range", func() {
		var s *string
		for _, c := range *s {
			fmt.Println(c)
		}
	})
	guard("arg", func() {
		var e *struct{}
		take(*e)
	})
	guard("write", func() {
		*p = 1
	})
	guard("fieldwrite", func() {
		t.val = 1
	})
	guard("nestedfieldwrite", func() {
		var lst List
		lst.head.next = &element{val: 11}
	})
	guard("nestedfieldread", func() {
		var lst List
		fmt.Println(lst.head.next.val)
	})
	guard("callarg", func() {
		take2(boom())
	})
	x := 7
	p = &x
	fmt.Println("live:", deref(p), compare(p))
}

func take(struct{}) {}

type element struct {
	next *element
	val  int
}

type List struct{ head, tail *element }

func take2(int) {}

func boom() int { panic("argument panic") }
