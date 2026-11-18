// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

// Test helpers shared by the quick tier (default build) and the full tier
// (`-tags full`): the files that define the full-tier tests are excluded from
// the push gate, so anything an untagged test file also needs lives here.

package gosource

// From packages_test.go.

func src(name, data string) Source { return Source{Name: name, Data: []byte(data)} }
