//go:build windows

package interp

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func ownedExecPlatform(f *os.File, _ int) (uint64, func(*exec.Cmd), func(), error) {
	h, err := duplicateInheritableHandle(f)
	if err != nil {
		return 0, nil, nil, err
	}
	apply := func(cmd *exec.Cmd) {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, h)
	}
	var once sync.Once
	return uint64(h), apply, func() { once.Do(func() { _ = syscall.CloseHandle(h) }) }, nil
}
