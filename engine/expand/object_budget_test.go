// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package expand_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
)

func TestValidObjectRejectsSharedDAGAtTraversalBudget(t *testing.T) {
	t.Parallel()

	type node struct {
		Left  *node `json:"left"`
		Right *node `json:"right"`
	}
	var root *node
	for range 19 {
		root = &node{Left: root, Right: root}
	}

	err := expand.ValidObject(root)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(err.Error(), "maximum traversal work")),
		qt.Commentf("error: %v", err))
}
