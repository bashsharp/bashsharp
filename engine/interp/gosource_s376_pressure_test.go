package interp

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// Opt-in Linux acceptance probe. Run in a fresh process inside a bounded
// memory cgroup, with an external watchdog and RSS measurement. The retained
// 96 MiB models live data; 512 transient 8 MiB buffers model allocation churn.
// This tests collector pacing, not the representation of interpreted values.
func TestS376MemoryPressure(t *testing.T) {
	if os.Getenv("S376_MEMORY_PRESSURE") != "1" {
		t.Skip("requires explicit bounded pressure runner")
	}
	release := goSourceRaiseGCPacing()
	defer release()
	started := time.Now()
	fmt.Printf("pressure budget_bytes=%d\n", debug.SetMemoryLimit(-1))
	live := make([]byte, 96<<20)
	for i := range live {
		live[i] = byte(i)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fmt.Printf("pressure initial_live_bytes=%d\n", before.HeapAlloc)
	for call := 1; call <= 512; call++ {
		garbage := make([]byte, 8<<20)
		for i := 0; i < len(garbage); i += 4096 {
			garbage[i] = byte(call)
		}
		runtime.KeepAlive(garbage)
		if call%32 == 0 {
			runtime.ReadMemStats(&after)
			fmt.Printf("pressure calls=%d heap_bytes=%d allocated_bytes=%d elapsed=%s\n", call, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc, time.Since(started))
		}
	}
	runtime.ReadMemStats(&after)
	runtime.GC()
	var final runtime.MemStats
	runtime.ReadMemStats(&final)
	fmt.Printf("pressure complete calls=512 live_bytes=%d bytes_per_call=%.2f allocs_per_call=%.3f elapsed=%s collections=%d\n", final.HeapAlloc, float64(after.TotalAlloc-before.TotalAlloc)/512, float64(after.Mallocs-before.Mallocs)/512, time.Since(started), final.NumGC-before.NumGC)
	runtime.KeepAlive(live)
	if final.HeapAlloc < 96<<20 {
		t.Fatal("retained payload missing from live heap measurement")
	}
}
