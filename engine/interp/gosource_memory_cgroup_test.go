package interp

import (
	"os"
	"testing"
)

func TestGoSourceCgroupMemory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  int64
	}{
		{"physical", nil, 12 << 30},
		{"v2 parent", map[string]string{"/proc/self/cgroup": "0::/parent/leaf\n", "/sys/fs/cgroup/parent/leaf/memory.max": "max", "/sys/fs/cgroup/parent/memory.max": "4294967296"}, 4 << 30},
		{"v1 leaf", map[string]string{"/proc/self/cgroup": "2:cpu,memory:/group\n", "/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712", "/sys/fs/cgroup/memory/group/memory.limit_in_bytes": "2147483648"}, 2 << 30},
		{"namespace root", map[string]string{"/sys/fs/cgroup/memory.max": "1073741824"}, 1 << 30},
		{"invalid", map[string]string{"/sys/fs/cgroup/memory.max": "-1"}, 12 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := goSourceCgroupMemory(12<<30, func(path string) ([]byte, error) {
				if s, ok := tc.files[path]; ok {
					return []byte(s), nil
				}
				return nil, os.ErrNotExist
			})
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
