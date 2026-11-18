// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #270; Story: #761; Story-ID: c8365c7b8c50
//
// //go:nointerface. Under GOEXPERIMENT=fieldtrack gc keeps a method marked
// //go:nointerface out of every interface method table (cmd/compile
// noder/lex.go pragmaFlag; reflectdata skips its itab entry; types2
// lookup reports "method is marked 'nointerface'"): the method stays
// callable directly and as a method expression, but a dynamic type
// assertion or type switch never finds it. Without the experiment gc
// ignores the pragma, so it is ignored here too.

import (
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

var goSourceNointerfaceByFile sync.Map // *syntax.File -> map[*syntax.BashPPFuncDecl]bool

// goSourceNointerfaceMethod reports whether decl is a method gc would omit
// from interface method sets.
func (r *Runner) goSourceNointerfaceMethod(decl *syntax.BashPPFuncDecl) bool {
	file := r.bashPPGoSourceFile
	if !r.bashPPGoSource || decl == nil || decl.Receiver == nil || file == nil || !goExperimentEnabled(r.envGet("GOEXPERIMENT"), "fieldtrack") {
		return false
	}
	marked, ok := goSourceNointerfaceByFile.Load(file)
	if !ok {
		set := map[*syntax.BashPPFuncDecl]bool{}
		for _, stmt := range file.Stmts {
			fn, isFunc := stmt.Cmd.(*syntax.BashPPFuncDecl)
			if !isFunc || fn.Receiver == nil {
				continue
			}
			for _, comment := range stmt.Comments {
				if fields := strings.Fields(comment.Text); len(fields) > 0 && fields[0] == "go:nointerface" {
					set[fn] = true
				}
			}
		}
		marked, _ = goSourceNointerfaceByFile.LoadOrStore(file, set)
	}
	return marked.(map[*syntax.BashPPFuncDecl]bool)[decl]
}

// goExperimentEnabled reads a GOEXPERIMENT list the way
// internal/buildcfg does for a default-off experiment: the last mention
// wins and a "no" prefix disables it.
func goExperimentEnabled(list, name string) bool {
	on := false
	for _, item := range strings.Split(list, ",") {
		switch strings.TrimSpace(item) {
		case name:
			on = true
		case "no" + name:
			on = false
		}
	}
	return on
}
