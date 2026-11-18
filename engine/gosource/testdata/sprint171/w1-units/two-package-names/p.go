package p

func F() { // ERROR "can inline F"
	print(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
}

func G() {
	F() // ERROR "inlining call to F"
	print(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
}

type t struct{ n int }

func (x t) m() int { return x.n } // ERROR "can inline t.m"
