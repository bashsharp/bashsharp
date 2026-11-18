//go:build full

package interp_test

import "testing"

// TestS243Issue4316Original is the unchanged upstream fixedbugs/issue4316.go
// body. Its 4096-deep named-pointer recursion catches evaluator work that
// grows with call depth; typedSendThreeModes retains the corpus's 60-second
// execution bound for its interpreted control.
func TestS243Issue4316Original(t *testing.T) {
	typedSendThreeModes(t, `package main

type Peano *Peano

func makePeano(n int) *Peano {
	if n == 0 {
		return nil
	}
	p := Peano(makePeano(n - 1))
	return &p
}

var countArg Peano
var countResult int

func countPeano() {
	if countArg == nil {
		countResult = 0
		return
	}
	countArg = *countArg
	countPeano()
	countResult++
}

var s = "(())"
var pT = 0

func p() {
	if pT >= len(s) {
		return
	}
	if s[pT] == '(' {
		pT += 1
		p()
		if pT < len(s) && s[pT] == ')' {
			pT += 1
		} else {
			return
		}
		p()
	}
}

func main() {
	countArg = makePeano(4096)
	countPeano()
	if countResult != 4096 {
		println("countResult =", countResult)
		panic("countResult != 4096")
	}
	p()
}
`)
}
