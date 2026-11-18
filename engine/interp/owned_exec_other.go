//go:build !windows && !unix

package interp

import (
	"errors"
	"os"
	"os/exec"
)

func ownedExecPlatform(_ *os.File, _ int) (uint64, func(*exec.Cmd), func(), error) {
	return 0, nil, nil, errors.New("owned exec handoff unsupported")
}
