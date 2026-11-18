package main

type S struct{ n int }

func (s *S) Inc() { s.n++ }

type pair struct {
	name string
	fn   func()
}

type C interface{ M() }

type CImpl struct{}

func (CImpl) M() {}

func sink(any) {}

func F1() {
	var s S
	fs := []func(){
		s.Inc,
	}
	for _, f := range fs {
		f()
	}
}

func F2() {
	var s S
	ps := []pair{
		{"a", s.Inc},
		{
			name: "b",
			fn:   s.Inc,
		},
	}
	sink(ps)
	one := []pair{{"c", s.Inc}}
	sink(one)
}

func F3() {
	var i C = struct {
		CImpl
	}{}
	i = struct{ CImpl }{}
	sink(i)
	m := map[string]int{
		"x": 1,

		"y": 2,
	}
	sink(m)
}

func F4() {
	sink(
		[]pair{
			{"d", nil},
		},
	)
	xs := append([]int{},
		1,
		2)
	sink(append(xs,
		[]int{
			3,
		}...))
}

func main() {
	F1()
	F2()
	F3()
	F4()
}
