//go:build !unix

package cli

func discoverInheritedFds() []int { return nil }
