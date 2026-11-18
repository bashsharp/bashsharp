package polyglot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// MSVC header discovery for the native (C/C++) islands on Windows.
//
// A stock clang on a Windows runner targets MSVC but knows no system include
// directories unless it runs inside a vcvarsall/Developer-prompt environment,
// so every hosted include fails with "'string.h' file not found". The islands
// therefore compile with the directories that prompt would have provided: an
// inherited INCLUDE wins; otherwise vswhere.exe (whose install location is
// fixed) names the newest VC tools, and the Windows SDK include tree is
// enumerated directly.

// errMSVCHeaders is the one line an operator can act on when no header source
// is discoverable.
var errMSVCHeaders = errors.New("polyglot: the C island needs the MSVC and Windows SDK headers: run from a vcvarsall/Developer prompt, or install Visual Studio Build Tools with the Windows SDK")

// msvcDiscovery is the raw material the Windows discovery gathers; assembling
// include directories from it is pure so tests drive it from fake vswhere
// output without running anything.
type msvcDiscovery struct {
	Include        string   // inherited INCLUDE, wins outright when set
	VSWhereOut     string   // vswhere -property installationPath stdout
	VCToolsVersion string   // Microsoft.VCToolsVersion.default.txt contents
	SDKRoot        string   // <ProgramFiles(x86)>\Windows Kits\10
	SDKVersions    []string // directory names under <SDKRoot>\Include
}

func (d msvcDiscovery) includeDirs() ([]string, error) {
	if dirs := splitIncludeList(d.Include); len(dirs) > 0 {
		return dirs, nil
	}
	install := firstLine(d.VSWhereOut)
	version := strings.TrimSpace(d.VCToolsVersion)
	sdk := newestVersionName(d.SDKVersions)
	if install == "" || version == "" || sdk == "" {
		return nil, errMSVCHeaders
	}
	return msvcIncludeDirs(install, version, d.SDKRoot, sdk), nil
}

// msvcIncludeDirs lists the include directories one VS installation and one
// Windows SDK version provide: the VC tools headers plus the four SDK trees a
// hosted build needs, in the same order vcvarsall composes INCLUDE.
func msvcIncludeDirs(vsInstall, vcToolsVersion, sdkRoot, sdkVersion string) []string {
	return []string{
		filepath.Join(vsInstall, "VC", "Tools", "MSVC", vcToolsVersion, "include"),
		filepath.Join(sdkRoot, "Include", sdkVersion, "ucrt"),
		filepath.Join(sdkRoot, "Include", sdkVersion, "shared"),
		filepath.Join(sdkRoot, "Include", sdkVersion, "um"),
		filepath.Join(sdkRoot, "Include", sdkVersion, "winrt"),
	}
}

func splitIncludeList(include string) []string {
	var dirs []string
	for _, d := range strings.Split(include, ";") {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}

// newestVersionName picks the highest dotted-numeric name (10.0.22621.0 over
// 10.0.19041.0); non-numeric components compare as zero.
func newestVersionName(names []string) string {
	best := ""
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if best == "" || compareDottedVersions(name, best) > 0 {
			best = name
		}
	}
	return best
}

func compareDottedVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isystemArgs(dirs []string) []string {
	args := make([]string, 0, 2*len(dirs))
	for _, dir := range dirs {
		args = append(args, "-isystem", dir)
	}
	return args
}

// nativeHostIncludeArgs returns the -isystem arguments a native-island compile
// needs on this host. Only a clang-family compiler on Windows needs help — a
// mingw gcc carries its own headers — and an inherited INCLUDE always wins
// over discovery.
func nativeHostIncludeArgs(goos, compiler string, environment *EnvironmentPlan) ([]string, error) {
	if goos != "windows" || !clangFamily(compiler) {
		return nil, nil
	}
	discovery := gatherMSVCDiscovery(environment)
	dirs, err := discovery.includeDirs()
	if err != nil {
		return nil, err
	}
	return isystemArgs(dirs), nil
}

func clangFamily(compiler string) bool {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(compiler)), ".exe")
	return strings.Contains(base, "clang")
}

func gatherMSVCDiscovery(environment *EnvironmentPlan) msvcDiscovery {
	discovery := msvcDiscovery{Include: launchEnvValue(environment, "INCLUDE")}
	if discovery.Include != "" {
		return discovery
	}
	programsX86 := os.Getenv("ProgramFiles(x86)")
	if programsX86 == "" {
		programsX86 = `C:\Program Files (x86)`
	}
	vswhere := filepath.Join(programsX86, "Microsoft Visual Studio", "Installer", "vswhere.exe")
	if _, err := os.Stat(vswhere); err == nil {
		out, err := exec.Command(vswhere, "-latest", "-products", "*",
			"-requires", "Microsoft.VisualStudio.Component.VC.Tools.x86.x64",
			"-property", "installationPath").Output()
		if err == nil {
			discovery.VSWhereOut = string(out)
		}
	}
	if install := firstLine(discovery.VSWhereOut); install != "" {
		version, err := os.ReadFile(filepath.Join(install, "VC", "Auxiliary", "Build", "Microsoft.VCToolsVersion.default.txt"))
		if err == nil {
			discovery.VCToolsVersion = string(version)
		}
	}
	discovery.SDKRoot = filepath.Join(programsX86, "Windows Kits", "10")
	if entries, err := os.ReadDir(filepath.Join(discovery.SDKRoot, "Include")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				discovery.SDKVersions = append(discovery.SDKVersions, entry.Name())
			}
		}
	}
	return discovery
}

// launchEnvValue reads a key from a plan's already-selected launch
// environment, case-insensitively as Windows environments are.
func launchEnvValue(environment *EnvironmentPlan, key string) string {
	if environment == nil {
		return ""
	}
	for _, entry := range environment.Env {
		if name, value, ok := strings.Cut(entry, "="); ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}
