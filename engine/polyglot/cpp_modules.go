package polyglot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// NativeModuleUnit is a source asset, never an executable build recipe.
// Source is canonical after discovery; Requires names explicit BMI providers.
type NativeModuleUnit struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Source   string   `json:"source"`
	Requires []string `json:"requires"`
}

type nativeModuleManifest struct {
	Version int                `json:"version"`
	Units   []NativeModuleUnit `json:"units"`
}

var nativeModuleName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*(:[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*)?$`)

// moduleAsset resolves symlinks before enforcing the project boundary. An
// asset cannot turn a relative declaration into an arbitrary external input.
func moduleAsset(root, dir, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("C++ module asset must be a relative path: %q", name)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	file, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("C++ module asset %q: %w", name, err)
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("C++ module asset escapes project: %q", name)
	}
	info, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("C++ module asset is not a regular file: %q", name)
	}
	return file, nil
}

func readNativeModules(root, dir, name string) ([]NativeModuleUnit, []string, error) {
	file, err := moduleAsset(root, dir, name)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	var manifest nativeModuleManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, fmt.Errorf("C++ module manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("C++ module manifest has trailing data")
	}
	if manifest.Version != 1 {
		return nil, nil, fmt.Errorf("unsupported C++ module manifest version %d", manifest.Version)
	}
	if len(manifest.Units) == 0 || len(manifest.Units) > 256 {
		return nil, nil, fmt.Errorf("C++ module manifest requires 1 to 256 units")
	}
	providers := map[string]int{}
	sources := map[string]bool{}
	files := []string{file}
	for i := range manifest.Units {
		unit := &manifest.Units[i]
		if !nativeModuleName.MatchString(unit.Name) || unit.Name == "std" || strings.HasPrefix(unit.Name, "std.") || strings.HasPrefix(unit.Name, "std:") {
			return nil, nil, fmt.Errorf("invalid or reserved C++ module name %q", unit.Name)
		}
		switch unit.Kind {
		case "interface", "internal-partition":
			if unit.Kind == "internal-partition" && !strings.Contains(unit.Name, ":") {
				return nil, nil, fmt.Errorf("internal partition %q needs a partition name", unit.Name)
			}
			if _, ok := providers[unit.Name]; ok {
				return nil, nil, fmt.Errorf("duplicate C++ module provider %q", unit.Name)
			}
			providers[unit.Name] = i
		case "implementation":
		default:
			return nil, nil, fmt.Errorf("invalid C++ module kind %q", unit.Kind)
		}
		unit.Source, err = moduleAsset(root, dir, unit.Source)
		if err != nil {
			return nil, nil, err
		}
		if sources[unit.Source] {
			return nil, nil, fmt.Errorf("duplicate C++ module source %q", unit.Source)
		}
		sources[unit.Source] = true
		files = append(files, unit.Source)
	}
	for _, unit := range manifest.Units {
		seen := map[string]bool{}
		for _, dep := range unit.Requires {
			if _, ok := providers[dep]; !ok {
				return nil, nil, fmt.Errorf("C++ module %q requires missing provider %q", unit.Name, dep)
			}
			if seen[dep] {
				return nil, nil, fmt.Errorf("duplicate C++ module dependency %q", dep)
			}
			seen[dep] = true
		}
		if unit.Kind == "implementation" && !seen[unit.Name] {
			return nil, nil, fmt.Errorf("C++ implementation %q must require its interface", unit.Name)
		}
	}
	var ordered []NativeModuleUnit
	state := make([]uint8, len(manifest.Units))
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 2 {
			return nil
		}
		if state[i] == 1 {
			return fmt.Errorf("C++ module dependency cycle at %q", manifest.Units[i].Name)
		}
		state[i] = 1
		for _, dep := range manifest.Units[i].Requires {
			if err := visit(providers[dep]); err != nil {
				return err
			}
		}
		state[i] = 2
		ordered = append(ordered, manifest.Units[i])
		return nil
	}
	for i := range manifest.Units {
		if err := visit(i); err != nil {
			return nil, nil, err
		}
	}
	return ordered, files, nil
}
