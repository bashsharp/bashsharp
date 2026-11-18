//go:build !windows

package interp

func nativeExecEnvForChild(env []string, execPath string) []string {
	return env
}
