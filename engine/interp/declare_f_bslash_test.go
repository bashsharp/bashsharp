// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

//go:build unix

package interp_test

// Sprint: #290 — declare -f must reparse: bash prints a command whose words
// the source continued with backslash-newlines on one line.

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestDeclareFJoinsEscapedNewlines(t *testing.T) {
	src := "f() {\n  A=1 B=2 \\\n    echo hi \\\n      there\n  case x in\n    x) C=3 \\\n        echo in;;\n  esac\n}\n" +
		"declare -f f\n" +
		"eval \"$(declare -f f | sed 's/^f /g /')\"; g\n"
	out, err := runScript(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	want := "f () \n{ \n    A=1 B=2 echo hi there;\n    case x in \n        x)\n            C=3 echo in\n        ;;\n    esac\n}\nhi there\nin\n"
	qt.Assert(t, qt.Equals(out, want))
}
