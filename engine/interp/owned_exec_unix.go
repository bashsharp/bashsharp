//go:build unix

package interp

import (
	"os"
	"os/exec"
)

func ownedExecPlatform(f *os.File, extraFiles int) (uint64, func(*exec.Cmd), func(), error) {
	// ExtraFiles[0] is fd 3 in the child.
	_ = os.Remove(f.Name())
	return uint64(3 + extraFiles), nil, nil, nil
}
