package interp

// Keep three quarters of the host/container allowance available for the
// dependency worker, non-Go mappings and other processes. This is a runtime
// soft limit (Sys-HeapReleased), not an RSS ceiling or a live-data bound.
// Unknown platforms get a conservative default. Explicit GOMEMLIMIT and a
// stricter embedding application's SetMemoryLimit always take precedence.
func goSourceMemoryBudget() int64 {
	const fallback = int64(512 << 20)
	if total := goSourceHostMemory(); total > 0 {
		return total / 4
	}
	return fallback
}
