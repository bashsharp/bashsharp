//go:build unix

package interp

import (
	"os"
	"testing"
)

func TestInheritedFdDoesNotOwnCallerDescriptor(t *testing.T) {
	caller, err := os.CreateTemp(t.TempDir(), "inherited-fd-")
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	if _, err := caller.WriteString("still open"); err != nil {
		t.Fatal(err)
	}
	logicalFD := int(caller.Fd())
	r := &Runner{inheritedFds: map[int]bool{logicalFD: true}}
	owned, ok := r.inheritedFd(logicalFD)
	if !ok {
		t.Fatal("registered open descriptor was not inherited")
	}
	if owned.Fd() == caller.Fd() {
		t.Fatal("runner adopted caller-owned descriptor without duplicating it")
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Seek(0, 0); err != nil {
		t.Fatalf("runner closed caller descriptor: %v", err)
	}
	var got [10]byte
	if _, err := caller.Read(got[:]); err != nil {
		t.Fatalf("caller descriptor unreadable after runner closed its copy: %v", err)
	}
	if string(got[:]) != "still open" {
		t.Fatalf("caller descriptor read %q, want still open", got)
	}
}
