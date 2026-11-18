package polyglot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const csharpGraphLimit = 8 << 20

// CSharpAssemblyGraph is the closed, versioned assembly input contract. Data is
// populated during discovery; paths are logical identities, never DLL searches.
type CSharpAssemblyGraph struct {
	Schema  string               `json:"schema"`
	Root    string               `json:"root"`
	Sources []CSharpAsset        `json:"sources"`
	Keys    []CSharpAsset        `json:"keys"`
	Nodes   []CSharpAssemblyNode `json:"nodes"`
}
type CSharpAsset struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data,omitempty"`
}
type CSharpAssemblyNode struct {
	ID             string                    `json:"id"`
	Name           string                    `json:"name"`
	Version        string                    `json:"version"`
	Culture        string                    `json:"culture"`
	PublicKeyToken string                    `json:"public_key_token"`
	Output         string                    `json:"output"`
	Sources        []string                  `json:"sources"`
	References     []CSharpAssemblyReference `json:"references"`
	Key            string                    `json:"key,omitempty"`
}
type CSharpAssemblyReference struct {
	Node    string   `json:"node"`
	Aliases []string `json:"aliases"`
}

var csharpAssetID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
var csharpAliasID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var csharpVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})\.(0|[1-9][0-9]{0,4})$`)

func csharpAssetPath(p string) error {
	if p == "" || strings.ContainsAny(p, "\\:\x00") || !utf8.ValidString(p) {
		return fmt.Errorf("invalid C# asset path %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return fmt.Errorf("invalid C# asset path %q", p)
		}
		base := strings.ToUpper(strings.Split(part, ".")[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[0-9]$`).MatchString(base) {
			return fmt.Errorf("reserved C# asset path %q", p)
		}
		for _, c := range part {
			if c < 32 || strings.ContainsRune(`<>"|?*`, c) {
				return fmt.Errorf("invalid C# asset path %q", p)
			}
		}
	}
	return nil
}

// strictCSharpJSON rejects duplicate members at every nesting level as well as
// unknown fields. Encoding/json alone silently accepts last-member wins.
func strictCSharpJSON(data []byte, out any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("C# manifest is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s := k.(string)
				if seen[s] {
					return fmt.Errorf("duplicate C# manifest field %q", s)
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing C# manifest data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

// readCSharpAsset disallows symlinks in the relative path. os.Root additionally
// confines path resolution even if a parent is concurrently renamed.
func readCSharpAsset(root *os.Root, path string) ([]byte, error) {
	if err := csharpAssetPath(path); err != nil {
		return nil, err
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("C# asset symlink %q", path)
		}
	}
	f, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("C# asset is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, csharpGraphLimit+1))
	if len(b) > csharpGraphLimit {
		return nil, fmt.Errorf("C# asset exceeds byte limit")
	}
	return b, err
}

// resolveCSharpAssemblies snapshots and verifies all user-declared bytes once.
// It authenticates content against the manifest, not upstream provenance.
func resolveCSharpAssemblies(dir, path string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := readCSharpAsset(root, path)
	if err != nil {
		return "", err
	}
	var g CSharpAssemblyGraph
	if err = strictCSharpJSON(data, &g); err != nil {
		return "", err
	}
	if len(g.Nodes) == 0 || len(g.Nodes) > 32 || len(g.Sources) > 128 || len(g.Keys) > 32 {
		return "", fmt.Errorf("C# assembly graph exceeds input limits")
	}
	total := 0
	base := filepath.Dir(filepath.FromSlash(path))
	for _, list := range [][]CSharpAsset{g.Sources, g.Keys} {
		for i := range list {
			a := &list[i]
			if len(a.Data) != 0 {
				return "", fmt.Errorf("manifest data must come from declared paths")
			}
			if err = csharpAssetPath(a.Path); err != nil {
				return "", err
			}
			a.Data, err = readCSharpAsset(root, filepath.ToSlash(filepath.Join(base, a.Path)))
			total += len(a.Data)
			if total > csharpGraphLimit {
				return "", fmt.Errorf("C# assembly graph exceeds byte limit")
			}
			if err != nil {
				return "", err
			}
		}
	}
	if err = g.validate(); err != nil {
		return "", err
	}
	sort.Slice(g.Sources, func(i, j int) bool { return g.Sources[i].ID < g.Sources[j].ID })
	sort.Slice(g.Keys, func(i, j int) bool { return g.Keys[i].ID < g.Keys[j].ID })
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	for i := range g.Nodes {
		sort.Slice(g.Nodes[i].References, func(a, b int) bool { return g.Nodes[i].References[a].Node < g.Nodes[i].References[b].Node })
		for j := range g.Nodes[i].References {
			sort.Strings(g.Nodes[i].References[j].Aliases)
		}
	}
	data, err = json.Marshal(g)
	return string(data), err
}

func (g CSharpAssemblyGraph) validate() error {
	fail := func(s string) error { return fmt.Errorf("C# assembly graph: %s", s) }
	if g.Schema != "csharp-assembly-graph-v1" {
		return fail("unsupported schema")
	}
	if len(g.Nodes) == 0 || len(g.Nodes) > 32 || len(g.Sources) > 128 || len(g.Keys) > 32 {
		return fail("node/source/key limit")
	}
	paths := map[string]bool{}
	sources := map[string]bool{}
	keys := map[string]bool{}
	usedSources := map[string]bool{}
	usedKeys := map[string]bool{}
	total := 0
	path := func(p string) error {
		if err := csharpAssetPath(p); err != nil {
			return err
		}
		p = strings.ToLower(p)
		if paths[p] {
			return fail("case-folded path collision")
		}
		paths[p] = true
		return nil
	}
	for n, list := range [][]CSharpAsset{g.Sources, g.Keys} {
		for _, a := range list {
			ids := sources
			if n == 1 {
				ids = keys
			}
			if !csharpAssetID.MatchString(a.ID) || ids[a.ID] {
				return fail("invalid or duplicate asset ID")
			}
			ids[a.ID] = true
			if err := path(a.Path); err != nil {
				return err
			}
			total += len(a.Data)
			if total > csharpGraphLimit {
				return fail("aggregate byte limit")
			}
			sum := sha256.Sum256(a.Data)
			if hex.EncodeToString(sum[:]) != a.SHA256 {
				return fail("asset digest mismatch: " + a.ID)
			}
			if n == 0 && !utf8.Valid(a.Data) {
				return fail("source is not UTF-8")
			}
		}
	}
	nodes := map[string]CSharpAssemblyNode{}
	identities := map[string]bool{}
	edges := 0
	for _, n := range g.Nodes {
		if !csharpAssetID.MatchString(n.ID) || nodes[n.ID].ID != "" || !csharpAssetID.MatchString(n.Name) {
			return fail("invalid or duplicate node identity")
		}
		if !csharpVersion.MatchString(n.Version) {
			return fail("invalid four-component version")
		}
		for _, part := range strings.Split(n.Version, ".") {
			var v int
			fmt.Sscan(part, &v)
			if v > 65534 {
				return fail("version component exceeds 65534")
			}
		}
		if n.Culture != "neutral" {
			return fail("only neutral culture supported")
		}
		if n.PublicKeyToken != "null" {
			b, e := hex.DecodeString(n.PublicKeyToken)
			if e != nil || len(b) != 8 || strings.ToLower(n.PublicKeyToken) != n.PublicKeyToken {
				return fail("invalid public key token")
			}
		}
		identity := n.Name + "," + n.Version + "," + n.Culture + "," + n.PublicKeyToken
		if identities[identity] {
			return fail("duplicate full assembly identity")
		}
		identities[identity] = true
		if err := path(n.Output); err != nil {
			return err
		}
		if !strings.HasSuffix(n.Output, ".dll") {
			return fail("output must be DLL")
		}
		if len(n.Sources) == 0 {
			return fail("node has no sources")
		}
		seen := map[string]bool{}
		for _, s := range n.Sources {
			if !sources[s] || seen[s] {
				return fail("missing or duplicate source")
			}
			seen[s] = true
			usedSources[s] = true
		}
		if n.Key != "" {
			if !keys[n.Key] || n.PublicKeyToken == "null" {
				return fail("invalid signing key")
			}
			usedKeys[n.Key] = true
		} else if n.PublicKeyToken != "null" {
			return fail("signed identity requires explicit key")
		}
		seen = map[string]bool{}
		for _, r := range n.References {
			edges++
			if seen[r.Node] || r.Node == n.ID || len(r.Aliases) == 0 {
				return fail("invalid direct reference")
			}
			seen[r.Node] = true
			aliases := map[string]bool{}
			for _, a := range r.Aliases {
				if !csharpAliasID.MatchString(a) || aliases[a] {
					return fail("invalid or duplicate alias")
				}
				aliases[a] = true
			}
		}
		nodes[n.ID] = n
	}
	if edges > 128 {
		return fail("edge limit")
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		n, ok := nodes[id]
		if !ok {
			return fail("missing referenced node")
		}
		if state[id] == 1 {
			return fail("cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, r := range n.References {
			if e := visit(r.Node); e != nil {
				return e
			}
		}
		state[id] = 2
		return nil
	}
	if e := visit(g.Root); e != nil {
		return e
	}
	if len(state) != len(nodes) || len(usedSources) != len(sources) || len(usedKeys) != len(keys) {
		return fail("unreachable assets")
	}
	return nil
}
