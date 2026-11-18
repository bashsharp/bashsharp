//go:build full

package interp_test

// Sprint: #374; Story-ID: 5568f906f766

import (
	"strings"
	"testing"
)

// `name = mustLoad(src).Name()` calls mustLoad to obtain the method's
// receiver while the assignment is still resolving its callee. When that call
// panics, the assignment is interrupted: the panic is the outcome. The callee
// lookup then has nothing to report, and the assignment must not read that as
// an undeclared callee (BASHPP-EASSIGN-CALL), which printed a second, wrong
// diagnostic and turned a recovered panic into exit status 2. The same holds
// for any other outcome the receiver evaluation records (exit, fatal error).
const s374InterruptedReceiverSource = `package main

import (
	"errors"
	"fmt"
)

type Package struct{ name string }

func (p *Package) Name() string { return p.name }

func (p *Package) Pair() (string, int) { return p.name, len(p.name) }

func load(src string) (*Package, error) {
	if src == "" {
		return nil, errors.New("empty source")
	}
	return &Package{name: src}, nil
}

func mustLoad(src string) *Package {
	pkg, err := load(src)
	if err != nil {
		panic(err)
	}
	return pkg
}

func name(src string) (name string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("recovered: %v", p)
		}
	}()
	name = "unset"
	name = mustLoad(src).Name()
	return name, nil
}

func pair(src string) (name string, size int, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("recovered: %v", p)
		}
	}()
	name, size = mustLoad(src).Pair()
	return name, size, nil
}

func main() {
	fmt.Println(name("p0"))
	fmt.Println(name(""))
	fmt.Println(pair("p1"))
	fmt.Println(pair(""))
	if RECOVER {
		return
	}
	var last string
	last = mustLoad("").Name()
	fmt.Println("unreachable", last)
}
`

func TestS374AssignFromMethodOnPanickingCallResultRecovered(t *testing.T) {
	source := strings.Replace(s374InterruptedReceiverSource, "RECOVER", "true", 1)
	got, err := runGoSourceIdentity(t, source, "")
	want := "p0 <nil>\nunset recovered: empty source\np1 2 <nil>\n 0 recovered: empty source\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}

func TestS374AssignFromMethodOnPanickingCallResultUnrecovered(t *testing.T) {
	source := strings.Replace(s374InterruptedReceiverSource, "RECOVER", "false", 1)
	got, _ := runGoSourceIdentity(t, source, "")
	if strings.Contains(got.stderr, "BASHPP-EASSIGN-CALL") || !strings.Contains(got.stderr, "panic: ") || !strings.Contains(got.stderr, "empty source") || strings.Contains(got.stdout, "unreachable") || got.status != 2 {
		t.Fatalf("outcome=%+v", got)
	}
}
