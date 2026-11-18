// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"io/fs"
	"os"
	"slices"
	"strings"
)

func (r *Runner) ensureDirFile(path string) {
	if r.dirFile != nil {
		return
	}
	r.dirFile, _ = openRunnerDir(path)
}

func (r *Runner) replaceDirFile(path string) {
	next, _ := openRunnerDir(path)
	r.closeDirFile()
	r.dirFile = next
}

func (r *Runner) closeDirFile() {
	if r.dirFile != nil {
		_ = r.dirFile.Close()
		r.dirFile = nil
	}
}

func readRunnerDir(file *os.File, path string) ([]fs.DirEntry, bool, error) {
	dir, ok, err := openRunnerDirAt(file, path)
	if !ok || err != nil {
		return nil, ok, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, true, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, true, nil
}
