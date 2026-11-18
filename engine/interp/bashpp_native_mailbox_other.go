//go:build !unix && !windows

package interp

func newBashPPCallbackMailbox() (*bashPPCallbackMailbox, error) { return nil, nil }

func bashPPMailboxOpenWorkerSource() string { return "" }
