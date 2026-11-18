package interp

import (
	pathpkg "path"
	"strconv"
	"strings"
)

// Read both the namespace root and the process's ancestry: a parent cgroup
// can be tighter than the leaf. v1 unlimited sentinels exceed physical RAM.
// Missing/unreadable controllers leave the physical-memory bound in force.
func goSourceCgroupMemory(total int64, read func(string) ([]byte, error)) int64 {
	apply := func(path string) {
		data, err := read(path)
		if err != nil {
			return
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err == nil && n > 0 && (total <= 0 || n < total) {
			total = n
		}
	}
	walk := func(root, relative, file string) {
		path := pathpkg.Join(root, pathpkg.Clean("/"+relative))
		for {
			apply(pathpkg.Join(path, file))
			if path == root {
				break
			}
			parent := pathpkg.Dir(path)
			if parent == path || !strings.HasPrefix(parent, root) {
				break
			}
			path = parent
		}
	}
	walk("/sys/fs/cgroup", "", "memory.max")
	walk("/sys/fs/cgroup/memory", "", "memory.limit_in_bytes")
	if data, err := read("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				continue
			}
			if parts[1] == "" {
				walk("/sys/fs/cgroup", parts[2], "memory.max")
			}
			for _, controller := range strings.Split(parts[1], ",") {
				if controller == "memory" {
					walk("/sys/fs/cgroup/memory", parts[2], "memory.limit_in_bytes")
				}
			}
		}
	}
	return total
}
