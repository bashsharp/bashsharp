package interp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPStreamJobKey struct{}

type bashPPStreamJob struct {
	group   int
	modules map[*polyglot.Module]bool
	mu      sync.Mutex
	active  map[*polyglot.Module]bool
}

// Reserve the real workers before starting any pipeline component. Command
// expansion is deliberately not run here: preflight must not duplicate command
// substitutions, redirects, or other shell side effects. Hidden dynamic stream
// calls fail closed at dispatch instead of moving workers after children start.
func (r *Runner) bashPPPrepareStreamPipeline(ctx context.Context, node *syntax.BinaryCmd) (context.Context, func(), error) {
	noop := func() {}
	if r.Dialect() != syntax.LangBashPP || ctx.Value(bashPPStreamJobKey{}) != nil {
		return ctx, noop, nil
	}
	counts := make(map[*polyglot.Module]int)
	stageModules := make(map[*polyglot.Module]bool)
	visiting := make(map[string]bool)
	var scan func(syntax.Node)
	var command func(string)
	command = func(word string) {
		if word == "" || visiting[word] {
			return
		}
		visiting[word] = true
		defer delete(visiting, word)
		if alias, ok := r.alias[word]; r.opts[optExpandAliases] && ok && len(alias.args) > 0 {
			command(alias.args[0].Lit())
			return
		}
		if body := r.Funcs[word]; body != nil {
			scan(body)
			return
		}
		module, name, ok := r.bashPPForeignCommand(word)
		if ok {
			if _, streaming := module.StreamSignature(name); streaming {
				stageModules[module] = true
			}
		}
	}
	scan = func(n syntax.Node) {
		syntax.Walk(n, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncDecl, *syntax.BashPPFuncDecl, *syntax.SourceBlock:
				return false
			case *syntax.CallExpr:
				if len(n.Args) > 0 {
					command(n.Args[0].Lit())
				}
			}
			return true
		})
	}
	var scanStage func(*syntax.Stmt)
	scanStage = func(st *syntax.Stmt) {
		if pipeline, ok := st.Cmd.(*syntax.BinaryCmd); ok && (pipeline.Op == syntax.Pipe || pipeline.Op == syntax.PipeAll) {
			scanStage(pipeline.X)
			scanStage(pipeline.Y)
			return
		}
		clear(stageModules)
		scan(st)
		for module := range stageModules {
			counts[module]++
		}
	}
	scanStage(node.X)
	scanStage(node.Y)
	if len(counts) == 0 {
		return ctx, noop, nil
	}
	modules := make([]*polyglot.Module, 0, len(counts))
	for m, count := range counts {
		if count > 1 {
			return ctx, noop, fmt.Errorf("foreign stream: one persistent module cannot serve multiple pipeline stages")
		}
		modules = append(modules, m)
	}
	// All jobs acquire module reservations in the same order, including jobs
	// whose textual stage order is reversed, so multi-module jobs cannot deadlock.
	sort.Slice(modules, func(i, j int) bool { return fmt.Sprintf("%p", modules[i]) < fmt.Sprintf("%p", modules[j]) })
	return r.bashPPStartStreamJob(ctx, modules)
}

func (r *Runner) bashPPStartStreamJob(ctx context.Context, modules []*polyglot.Module) (context.Context, func(), error) {
	job := &bashPPStreamJob{modules: make(map[*polyglot.Module]bool), active: make(map[*polyglot.Module]bool)}
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg != nil && bg.pgrpFixed {
		job.group = int(bg.pgrp.Load())
	}
	joined := make([]*polyglot.Module, 0, len(modules))
	leave := func() {
		for i := len(joined) - 1; i >= 0; i-- {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			if err := joined[i].LeaveProcessGroup(cleanupCtx); err != nil {
				// A worker with unproven group ownership must never be reused.
				_ = joined[i].Close()
				r.errf("foreign stream process group cleanup: %v\n", err)
			}
			cancel()
		}
	}
	for _, module := range modules {
		group, err := module.JoinProcessGroup(ctx, job.group)
		if err != nil {
			leave()
			return ctx, func() {}, fmt.Errorf("foreign stream process group: %w", err)
		}
		joined = append(joined, module)
		job.modules[module] = true
		job.group = group
	}
	cleanupOS, err := r.bashPPStreamGroupLifecycle(ctx, job.group)
	if err != nil {
		leave()
		return ctx, func() {}, err
	}
	if bg != nil {
		bg.streamPgrp.Store(int64(job.group))
		bg.pgrp.Store(int64(job.group))
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cleanupOS()
			if bg != nil {
				bg.streamPgrp.CompareAndSwap(int64(job.group), 0)
			}
			leave()
		})
	}
	return context.WithValue(ctx, bashPPStreamJobKey{}, job), cleanup, nil
}

// bashPPJoinStreamJob is called by a streaming command adapter before opening
// an iterator. A preflight reservation covers the whole pipeline, rather than
// releasing the worker while a sibling still belongs to its process group.
func (r *Runner) bashPPJoinStreamJob(ctx context.Context, module *polyglot.Module) (func(), error) {
	if job, _ := ctx.Value(bashPPStreamJobKey{}).(*bashPPStreamJob); job != nil {
		if !job.modules[module] {
			return nil, fmt.Errorf("foreign stream: dynamic pipeline command was not reserved before launch")
		}
		job.mu.Lock()
		if job.active[module] {
			job.mu.Unlock()
			return nil, fmt.Errorf("foreign stream: persistent module already serves a pipeline stage")
		}
		job.active[module] = true
		job.mu.Unlock()
		return func() { job.mu.Lock(); delete(job.active, module); job.mu.Unlock() }, nil
	}
	if ctx.Value(pipelineExecCtxKey{}) != nil {
		return nil, fmt.Errorf("foreign stream: dynamic pipeline command requires a statically resolved stream stage")
	}
	_, cleanup, err := r.bashPPStartStreamJob(ctx, []*polyglot.Module{module})
	return cleanup, err
}
