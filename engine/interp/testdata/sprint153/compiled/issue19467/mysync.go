package mysync

import "runtime"

type WaitGroup struct{ PCs []uintptr }

func (w *WaitGroup) Add()  { w.PCs = make([]uintptr, runtime.Callers(1, make([]uintptr, 16))) }
func (w *WaitGroup) Done() { w.Add() }
