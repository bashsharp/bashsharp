// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import "sync"

// Struct and map storage shared between Bash++ tasks.
//
// The interpreter keeps an original Go struct — and an original Go map — as
// one Go `map[string]any` beside a `map[string]*bashPPCollectionMeta` that
// carries each entry's layout. A GoSource task shares the cells its closure
// captures with its parent (see gosource_task_capture.go), so two tasks can
// reach one such map at the same time. For a struct that is legal Go without
// any further synchronization: the fields are separate words, and a sender
// writing `c.sv` while a receiver reads `c.rc` races on nothing. The Go map
// underneath, however, is one object, and the runtime's concurrent-map check
// turns that legal program into `fatal error: concurrent map writes` — an
// interpreter artifact, not a property of the program.
//
// bashPPStorageMu is the same discipline goSourceAtomicMutex uses: one lock
// for the process rather than one per value, which is stronger than Go
// promises and therefore never wrong. Every critical section below is a leaf
// — a single map read, write, delete or shallow snapshot, never a callback —
// so it can never block on interpreter work and can never nest. Iteration
// sites take a snapshot of the keys or entries and walk the snapshot, so a
// recursive walk (a deep compare, a clone) never holds the read lock while it
// descends into a nested map.
//
// A program that races on the SAME field still races here, exactly as it
// races in Go: this lock keeps the interpreter's own storage sound, it does
// not add synchronization the program did not write.
var bashPPStorageMu sync.RWMutex

func bashPPStorageGet(m map[string]any, key string) (any, bool) {
	bashPPStorageMu.RLock()
	value, found := m[key]
	bashPPStorageMu.RUnlock()
	return value, found
}

func bashPPStorageSet(m map[string]any, key string, value any) {
	bashPPStorageMu.Lock()
	m[key] = value
	bashPPStorageMu.Unlock()
}

// bashPPStorageSetField writes one entry and its layout under a single
// critical section, so a reader never observes a value beside a stale layout.
// A nil layout map skips the layout write.
func bashPPStorageSetField(m map[string]any, layout map[string]*bashPPCollectionMeta, key string, value any, child *bashPPCollectionMeta) {
	bashPPStorageMu.Lock()
	m[key] = value
	if layout != nil {
		if child == nil {
			delete(layout, key)
		} else {
			layout[key] = child
		}
	}
	bashPPStorageMu.Unlock()
}

func bashPPStorageDelete(m map[string]any, layout map[string]*bashPPCollectionMeta, key string) {
	bashPPStorageMu.Lock()
	delete(m, key)
	if layout != nil {
		delete(layout, key)
	}
	bashPPStorageMu.Unlock()
}

func bashPPStorageLen(m map[string]any) int {
	bashPPStorageMu.RLock()
	n := len(m)
	bashPPStorageMu.RUnlock()
	return n
}

// bashPPStorageKeys returns the entry names at the moment of the call, in map
// order; callers sort when order matters.
func bashPPStorageKeys(m map[string]any) []string {
	bashPPStorageMu.RLock()
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	bashPPStorageMu.RUnlock()
	return keys
}

// bashPPStorageSnapshot returns a shallow copy of the entries at the moment of
// the call. Nested composites are the same objects; only this level is copied.
func bashPPStorageSnapshot(m map[string]any) map[string]any {
	bashPPStorageMu.RLock()
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = value
	}
	bashPPStorageMu.RUnlock()
	return out
}

func bashPPLayoutGet(layout map[string]*bashPPCollectionMeta, key string) *bashPPCollectionMeta {
	if layout == nil {
		return nil
	}
	bashPPStorageMu.RLock()
	child := layout[key]
	bashPPStorageMu.RUnlock()
	return child
}

func bashPPLayoutSet(layout map[string]*bashPPCollectionMeta, key string, child *bashPPCollectionMeta) {
	bashPPStorageMu.Lock()
	layout[key] = child
	bashPPStorageMu.Unlock()
}

func bashPPLayoutSnapshot(layout map[string]*bashPPCollectionMeta) map[string]*bashPPCollectionMeta {
	bashPPStorageMu.RLock()
	out := make(map[string]*bashPPCollectionMeta, len(layout))
	for key, child := range layout {
		out[key] = child
	}
	bashPPStorageMu.RUnlock()
	return out
}
