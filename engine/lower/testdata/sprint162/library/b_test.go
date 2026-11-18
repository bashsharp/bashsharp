package library

import "strings"
import "testing"

func Upper(t Thing) string {
	return strings.ToUpper(t.String())
}

func TestInitialized(t *testing.T) {
	if !Initialized {
		t.Fatal("native init did not run")
	}
}
