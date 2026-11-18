//go:build unix

package interp_test

import (
	"os"
	"os/signal"
	"syscall"
)

func setTestProcessGroup() {
	_ = syscall.Setpgid(0, 0)
}

func notifyTestUserSignal(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGUSR1)
}
