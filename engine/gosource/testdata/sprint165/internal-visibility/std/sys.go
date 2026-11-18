// testdir:intrinsic.go's shape: the testdir harness compiles the .dir
// program with -p main, and main is a standard identity.
package main

import T "internal/runtime/sys"

func main() { _ = T.Bswap64(1) }
