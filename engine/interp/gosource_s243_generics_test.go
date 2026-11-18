//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Sprint: #243; Story: #671; Story-ID: 56d156f9118e

func TestS243OriginalGenericRangeRoot(t *testing.T) {
	for _, tc := range []struct {
		name, path, digest string
	}{
		{"typeparam_mdempsky_17", filepath.Join("typeparam", "mdempsky", "17.go"), "8398f0ebdf833c55e76a3eb0afd902540893a7db0f10d84284e23243f9dae7d4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", tc.path))
			if err != nil {
				t.Fatal(err)
			}
			if tc.digest != "" && fmt.Sprintf("%x", sha256.Sum256(source)) != tc.digest {
				t.Fatalf("original %s bytes changed", tc.path)
			}
			differGoSource(t, string(source), nil, "")
		})
	}
}

func TestS243GenericRangeBindingIsPerInstantiation(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "reflect")
func names[T any](values []T) []string {
	var out []string
	for _, value := range []T{values[0]} { out = append(out, reflect.TypeOf(value).Name()) }
	return out
}
type I int
type S string
func main() { fmt.Println(names([]I{1}), names([]S{"x"})) }
`, nil, "")
}
