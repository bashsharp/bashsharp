// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interactive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
)

// sh's plain terminal loop must persist commands between interactive shell
// sessions. A later fc invocation can then list a prior session's fc command.
func TestPlainTerminalPersistsInteractiveHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	var out, errOut bytes.Buffer
	r, err := interp.New(
		interp.Dir(dir),
		interp.Env(expand.ListEnviron("HOME="+dir, "HISTFILE="+path, "PATH=/bin:/usr/bin")),
		interp.StdIO(strings.NewReader(""), &out, &errOut),
		interp.Interactive(true),
		interp.WithPosixMode(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), Options{
		Runner: r, PosixMode: true, PlainTerminal: true, HistoryFile: path,
		Stdin:  strings.NewReader("echo marker\nfc -l -n\n"),
		Stdout: &out, Stderr: &errOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"echo marker\n", "fc -l -n\n"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("plain terminal history %q lacks %q", data, want)
		}
	}
}
