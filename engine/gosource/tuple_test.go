package gosource

import (
	"strings"
	"testing"
)

func TestTupleVarInvalidProgramsRemainRejected(t *testing.T) {
	for name, source := range map[string]string{
		"arity":         `package p;func f(){var a,b=1;_,_=a,b}`,
		"redeclare":     `package p;func f(){a:=1;var a,b=map[int]int{}[0];_,_=a,b}`,
		"before_scope":  `package p;func f(){var a,b=map[int]int{}[a];_,_=a,b}`,
		"comma_ok_type": `package p;func f(){var a,b int=map[int]int{}[0];_,_=a,b}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Parse(strings.NewReader(source), "original.go", Options{})
			if err == nil || p != nil {
				t.Fatalf("invalid original accepted: %v %v", p, err)
			}
		})
	}
}
