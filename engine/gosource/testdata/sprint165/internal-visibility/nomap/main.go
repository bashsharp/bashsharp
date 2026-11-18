// An ordinary program with no declared identity (no Options.ImportPath):
// its imports are decided by the fallback importer's own rule, before and
// after Sprint 165. With lower's module importer that rule is the directory
// rule, which refuses a top-level internal package from a directory outside
// GOROOT.
package main

import "internal/abi"

func main() { var _ abi.Type }
