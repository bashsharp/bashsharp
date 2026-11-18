// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build linux

package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestExternalChildObservesClosedStandardOutputDescriptors(t *testing.T) {
	const src = `
(
	/bin/sh -c '[ -e /proc/self/fd/1 ] && exit 0 || exit 127' >&-
)
printf 'stdout=%s\n' "$?"
(
	/bin/sh -c '[ -e /proc/self/fd/2 ] && exit 0 || exit 127' 2>&-
)
printf 'stderr=%s\n' "$?"
`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner, err := interp.New(interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "stdout=127\nstderr=127\n"; got != want {
		t.Fatalf("closed descriptor state was not inherited:\nwant: %q\ngot:  %q", want, got)
	}
}
