//go:build !unix

package interp_test

import "os"

func setTestProcessGroup() {}

func notifyTestUserSignal(chan<- os.Signal) {}
