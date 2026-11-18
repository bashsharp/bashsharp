package interp

// Sprint: #247; Story: #673; Story-ID: f24307569417

import (
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPGoSourceFreeNamesResult struct {
	free  map[string]bool
	exact bool
}

type bashPPGoSourceFreeNamesEntry struct {
	mu      sync.Mutex
	byBound map[string]bashPPGoSourceFreeNamesResult
}

// bashPPGoSourceFreeNamesCache maps a parsed body, held weakly so a finished
// program's syntax tree is not retained, to its analysed free names per bound
// set. A body's entry is dropped when the body is collected.
var bashPPGoSourceFreeNamesCache sync.Map // weak.Pointer[syntax.Block] -> *bashPPGoSourceFreeNamesEntry

func bashPPGoSourceFreeNamesMemo(body *syntax.Block, bound map[string]bool) (map[string]bool, bool) {
	names := make([]string, 0, len(bound))
	for name := range bound {
		names = append(names, name)
	}
	slices.Sort(names)
	boundKey := strings.Join(names, "\x00")
	key := weak.Make(body)
	value, loaded := bashPPGoSourceFreeNamesCache.Load(key)
	if !loaded {
		value, loaded = bashPPGoSourceFreeNamesCache.LoadOrStore(key, &bashPPGoSourceFreeNamesEntry{byBound: make(map[string]bashPPGoSourceFreeNamesResult)})
		if !loaded {
			runtime.AddCleanup(body, func(key weak.Pointer[syntax.Block]) {
				bashPPGoSourceFreeNamesCache.Delete(key)
			}, key)
		}
	}
	entry := value.(*bashPPGoSourceFreeNamesEntry)
	entry.mu.Lock()
	result, ok := entry.byBound[boundKey]
	entry.mu.Unlock()
	if ok {
		return result.free, result.exact
	}
	free, exact := bashPPGoSourceFreeNamesWalk(body, bound)
	entry.mu.Lock()
	entry.byBound[boundKey] = bashPPGoSourceFreeNamesResult{free: free, exact: exact}
	entry.mu.Unlock()
	return free, exact
}
