// Copyright (c) 2019, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"

	"golang.org/x/sys/windows"
)

func TestRunnerGlobMsysPWD(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tdir, "README.md"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "notes.txt"), nil, 0o666); err != nil {
		t.Fatal(err)
	}

	file := parse(t, nil, fmt.Sprintf("cd %s\nprintf '<%%s>\\n' *.md\n", singleQuote(msysPath(t, tdir))))
	var b bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &b, &b))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "<README.md>\n"; got != want {
		t.Fatalf("wrong glob output\nwant: %q\ngot:  %q", want, got)
	}
}

func msysPath(t *testing.T, path string) string {
	t.Helper()
	vol := filepath.VolumeName(path)
	if len(vol) < 2 || vol[1] != ':' {
		t.Fatalf("expected drive path, got %q", path)
	}
	drive := vol[0]
	if 'A' <= drive && drive <= 'Z' {
		drive += 'a' - 'A'
	}
	rest := strings.TrimPrefix(path[len(vol):], `\`)
	rest = strings.ReplaceAll(rest, `\`, "/")
	if rest == "" {
		return "/" + string(drive)
	}
	return "/" + string(drive) + "/" + rest
}

func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shortPathName is used for testing against DOS short names.
//
// Only used for testing, so we assume that a short path always fits in
// 2*len(path) in UTF-16.
func shortPathName(path string) (string, error) {
	src, err := windows.UTF16FromString(path)
	if err != nil {
		return "", err
	}
	dst := make([]uint16, len(src)*2)
	if _, err := windows.GetShortPathName(&src[0], &dst[0], uint32(len(dst))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(dst), nil
}
