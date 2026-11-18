package polyglot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Graph compilation has a distinct content-only namespace. Unlike the legacy
// single-unit memo, every invocation hashes provider and reference bytes.
func (c CSharp) graphToolchain() (string, error) {
	exe, err := exec.LookPath(c.executable())
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	files := []string{exe}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".dll") || strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".so") || strings.HasSuffix(e.Name(), ".dylib")) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	refs, err := filepath.Glob(filepath.Join(dir, "ref", "*.dll"))
	if err != nil || len(refs) == 0 {
		return "", fmt.Errorf("C# graph requires pinned provider ref/*.dll")
	}
	files = append(files, refs...)
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, f)
		sum := sha256.Sum256(b)
		fmt.Fprintf(h, "%s\x00%x\x00", filepath.ToSlash(rel), sum)
	}
	// Leading arguments may select a different provider; unsupported wrappers
	// cannot silently claim the above executable's framework identity.
	if len(leadingArgs(c.Environment)) != 0 {
		return "", fmt.Errorf("C# graphs require a direct pinned pwsh executable")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CSharpGraphDiagnostic retains Roslyn's diagnostic identity and logical source
// location. A failed prerequisite is distinct from a failed root compilation.
type CSharpGraphDiagnostic struct {
	Location string `json:"location"`
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
}
type CSharpGraphError struct {
	Stage       string                  `json:"stage"`
	Diagnostics []CSharpGraphDiagnostic `json:"diagnostics"`
}

func (e *CSharpGraphError) Error() string {
	var lines []string
	for _, d := range e.Diagnostics {
		lines = append(lines, fmt.Sprintf("%s:%d:%d: %s %s: %s [assembly %s]", d.Path, d.Line, d.Column, d.Severity, d.ID, d.Message, e.Stage))
	}
	return strings.Join(lines, "\n")
}

type csharpGraphOutput struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Identity string `json:"identity"`
}
type csharpGraphReceipt struct {
	Key     string              `json:"key"`
	Outputs []csharpGraphOutput `json:"outputs"`
	Exports []Export            `json:"exports"`
}
type csharpGraphUnit struct {
	csharpUnit
	dependencies []string
	graph        CSharpAssemblyGraph
	key          string
	dir          string
}

func csharpIdentity(n CSharpAssemblyNode) string {
	return n.Name + ", Version=" + n.Version + ", Culture=" + n.Culture + ", PublicKeyToken=" + n.PublicKeyToken
}
func (u csharpGraphUnit) receipt() ([]Export, bool) {
	b, e := os.ReadFile(filepath.Join(u.dir, "receipt.json"))
	if e != nil {
		return nil, false
	}
	var r csharpGraphReceipt
	if strictCSharpJSON(b, &r) != nil || r.Key != u.key || len(r.Outputs) != len(u.graph.Nodes) || len(r.Exports) == 0 {
		return nil, false
	}
	expected := map[string]string{}
	for _, n := range u.graph.Nodes {
		expected[n.Output] = csharpIdentity(n)
	}
	for _, o := range r.Outputs {
		if expected[o.Path] == "" || expected[o.Path] != o.Identity {
			return nil, false
		}
		delete(expected, o.Path)
		b, e := os.ReadFile(filepath.Join(u.dir, filepath.FromSlash(o.Path)))
		if e != nil {
			return nil, false
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != o.SHA256 {
			return nil, false
		}
	}
	return r.Exports, true
}
func (c CSharp) graphUnit(source, graph string) (csharpGraphUnit, error) {
	var u csharpGraphUnit
	if len(graph) > 2*csharpGraphLimit {
		return u, fmt.Errorf("C# embedded graph exceeds limit")
	}
	if e := strictCSharpJSON([]byte(graph), &u.graph); e != nil {
		return u, e
	}
	if e := u.graph.validate(); e != nil {
		return u, e
	}
	// Observer diagnostics use one stable logical path; absolute source roots
	// must not become assembly names or cache identity.
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		if m := csharpLineDirective.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			lines[i] = "#line " + m[1] + " \"observer.bsh\""
		}
	}
	generated, typ, e := csharpGenerate(strings.Join(lines, "\n"))
	if e != nil {
		return u, e
	}
	provider, e := c.graphToolchain()
	if e != nil {
		return u, e
	}
	sum := sha256.Sum256([]byte("bashpp-csharp-graph-v1\x00" + runtime.GOOS + "/" + runtime.GOARCH + "\x00" + provider + "\x00" + csharpGraphCompile + "\x00" + csharpCompilePrelude + "\x00" + graph + "\x00" + generated))
	u.key = hex.EncodeToString(sum[:])
	dir, e := c.cacheDir()
	if e != nil {
		return u, e
	}
	u.dir = filepath.Join(dir, "graph-"+u.key)
	u.source, u.typeName = generated, typ
	// Runtime supports one version per simple name. Compilation itself permits
	// multiple versions so a genuine CS1705 diagnostic is never masked.
	for _, n := range u.graph.Nodes {
		p := filepath.Join(u.dir, filepath.FromSlash(n.Output))
		if n.ID == u.graph.Root {
			u.assembly = p
		} else {
			u.dependencies = append(u.dependencies, p)
		}
	}
	return u, nil
}
func (c CSharp) ensureGraph(ctx context.Context, source, graph string) (csharpGraphUnit, []Export, error) {
	if err := ctx.Err(); err != nil {
		return csharpGraphUnit{}, nil, err
	}
	u, e := c.graphUnit(source, graph)
	if e != nil {
		return u, nil, e
	}
	if exports, ok := u.receipt(); ok {
		return u, exports, nil
	}
	// Serialize publishers; a crashed publisher leaves an explicit bounded
	// failure rather than permitting partial entries or racing quarantine.
	lock := u.dir + ".lock"
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		e = os.Mkdir(lock, 0700)
		if e == nil {
			break
		}
		if !os.IsExist(e) {
			return u, nil, e
		}
		select {
		case <-ctx.Done():
			return u, nil, ctx.Err()
		case <-deadline.C:
			return u, nil, fmt.Errorf("C# graph cache publisher lock timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer os.Remove(lock)
	if exports, ok := u.receipt(); ok {
		return u, exports, nil
	}
	stage, e := os.MkdirTemp(filepath.Dir(u.dir), ".graph-")
	if e != nil {
		return u, nil, e
	}
	defer os.RemoveAll(stage)
	req := map[string]any{"graph": u.graph, "source": u.source, "type": u.typeName, "dir": stage, "key": u.key}
	b, e := json.Marshal(req)
	if e != nil {
		return u, nil, e
	}
	name, args := workerExecArgs(c.executable(), append(leadingArgs(c.Environment), append(powerShellBaseArgs(), csharpCompilePrelude+csharpGraphCompile)...))
	cmd := exec.CommandContext(ctx, name, args...)
	c.configure(cmd)
	cmd.Stdin = bytes.NewReader(b)
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	csharpCompiles.Add(1)
	if e = cmd.Run(); e != nil {
		var failure CSharpGraphError
		if json.Unmarshal(bytes.TrimSpace(errout.Bytes()), &failure) == nil && failure.Stage != "" && len(failure.Diagnostics) > 0 {
			return u, nil, &failure
		}
		return u, nil, fmt.Errorf("C# assembly graph: %s", strings.TrimSpace(errout.String()+out.String()))
	}
	staged := u
	staged.dir = stage
	exports, ok := staged.receipt()
	if !ok {
		return u, nil, fmt.Errorf("C# graph produced an invalid output receipt")
	}
	if e = os.Rename(stage, u.dir); e != nil {
		if winner, ok := u.receipt(); ok {
			return u, winner, nil
		}
		// A corrupt cache is never repaired in place while a worker might use it.
		// Quarantine the entire invalid entry, then publish the complete new tree.
		bad := stage + "-invalid"
		if e = os.Rename(u.dir, bad); e != nil {
			return u, nil, e
		}
		defer os.RemoveAll(bad)
		if e = os.Rename(stage, u.dir); e != nil {
			return u, nil, e
		}
	}
	return u, exports, nil
}
func (c CSharp) graphPayload(plan Plan) string {
	if plan.CSharpAssemblies != "" {
		return plan.CSharpAssemblies
	}
	if c.Environment != nil {
		return c.Environment.CSharpAssemblies
	}
	return ""
}

// This compiler path is used only for explicitly admitted graphs. Its closed
// options are part of the cache key; no arbitrary compiler switches are accepted.
const csharpGraphCompile = `
$ErrorActionPreference = 'Stop'
try {
 $req = [Console]::In.ReadToEnd() | ConvertFrom-Json -AsHashtable
 $g = $req.graph
 $dir = [string]$req.dir
 $sources = @{}; $keys = @{}; $nodes = @{}; $built = @{}; $outputs = @()
 foreach ($a in $g.sources) { $sources[$a.id] = $a }
 foreach ($a in $g.keys) {
   $p = [IO.Path]::Combine($dir, 'keys', [string]$a.path)
   [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($p))
   [IO.File]::WriteAllBytes($p,[Convert]::FromBase64String([string]$a.data))
   $keys[$a.id] = $p
 }
 foreach ($n in $g.nodes) { $nodes[$n.id] = $n }
 $framework = [System.Collections.Generic.List[Microsoft.CodeAnalysis.MetadataReference]]::new()
 foreach ($p in @(Get-ChildItem -LiteralPath ([IO.Path]::Combine($PSHOME,'ref')) -Filter '*.dll' | Sort-Object Name)) {
   $framework.Add([Microsoft.CodeAnalysis.MetadataReference]::CreateFromFile($p.FullName))
 }
 function Build-GraphNode([string]$id) {
   if ($built.ContainsKey($id)) { return }
   $n = $nodes[$id]
   foreach ($r in $n.references) { Build-GraphNode ([string]$r.node) }
   $trees = [System.Collections.Generic.List[Microsoft.CodeAnalysis.SyntaxTree]]::new()
   $parse = [Microsoft.CodeAnalysis.CSharp.CSharpParseOptions]::Default.WithLanguageVersion([Microsoft.CodeAnalysis.CSharp.LanguageVersion]::CSharp12)
   foreach ($sid in $n.sources) {
     $a = $sources[$sid]
     $text = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([string]$a.data))
     $trees.Add([Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText($text,$parse,[string]$a.path,[Text.Encoding]::UTF8))
   }
   if ($id -eq $g.root) {
     $text = Normalize-FenceSource ([string]$req.source) ([string]$req.type)
     $trees.Add([Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText($text,$parse,'observer.cs',[Text.Encoding]::UTF8))
   }
   $refs = [System.Collections.Generic.List[Microsoft.CodeAnalysis.MetadataReference]]::new()
   $refs.AddRange($framework)
   foreach ($r in $n.references) {
     $ref = [Microsoft.CodeAnalysis.MetadataReference]::CreateFromFile([string]$built[$r.node])
     $refs.Add($ref.WithAliases([string[]]$r.aliases))
   }
   $options = [Microsoft.CodeAnalysis.CSharp.CSharpCompilationOptions]::new([Microsoft.CodeAnalysis.OutputKind]::DynamicallyLinkedLibrary)
   $options = $options.WithAllowUnsafe($true).WithDeterministic($true).WithOptimizationLevel([Microsoft.CodeAnalysis.OptimizationLevel]::Release).WithNullableContextOptions([Microsoft.CodeAnalysis.NullableContextOptions]::Disable)
   if ($n.key) { $options = $options.WithCryptoKeyFile([string]$keys[$n.key]).WithStrongNameProvider([Microsoft.CodeAnalysis.DesktopStrongNameProvider]::new()).WithPublicSign($false).WithDelaySign($false) }
   $comp = [Microsoft.CodeAnalysis.CSharp.CSharpCompilation]::Create([string]$n.name,$trees,$refs,$options)
   $path = [IO.Path]::Combine($dir,[string]$n.output)
   [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path))
   $stream = [IO.File]::Create($path)
   try { $result = $comp.Emit($stream) } finally { $stream.Dispose() }
   if (-not $result.Success) {
     $lines = @()
     foreach ($d in $result.Diagnostics) {
       if ($d.Severity -eq [Microsoft.CodeAnalysis.DiagnosticSeverity]::Error) {
         $s = $d.Location.GetMappedLineSpan()
         $location = if ($d.Location.Kind -eq [Microsoft.CodeAnalysis.LocationKind]::None) { 'none' } else { 'source' }
         $lines += @{location=$location;id=$d.Id;severity='error';path=$s.Path;line=($s.StartLinePosition.Line+1);column=($s.StartLinePosition.Character+1);message=$d.GetMessage([Globalization.CultureInfo]::InvariantCulture)}
       }
     }
     throw (@{stage=$id;diagnostics=@($lines)} | ConvertTo-Json -Depth 8 -Compress)
   }
   $identity = [Reflection.AssemblyName]::GetAssemblyName($path).FullName
   $expected = '{0}, Version={1}, Culture={2}, PublicKeyToken={3}' -f $n.name,$n.version,$n.culture,$n.public_key_token
   if ($identity -cne $expected) { throw ('assembly identity mismatch: '+$identity+' expected '+$expected) }
   $built[$id] = $path
 }
 Build-GraphNode ([string]$g.root)
 $names = @{}
 foreach ($n in $g.nodes) {
   if ($names.ContainsKey($n.name)) { throw 'C# graph runtime does not support multiple versions of one assembly name' }
   $names[$n.name] = $true
   $path = [string]$built[$n.id]
   $outputs += @{path=$n.output;sha256=([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([IO.File]::ReadAllBytes($path)))).ToLowerInvariant();identity=[Reflection.AssemblyName]::GetAssemblyName($path).FullName}
 }
 $loadContext = [System.Runtime.Loader.AssemblyLoadContext]::new(('graph-' + [guid]::NewGuid().ToString('N')), $false)
 foreach ($n in $g.nodes) { if ($n.id -ne $g.root) { [void]$loadContext.LoadFromAssemblyPath([string]$built[$n.id]) } }
 $asm = $loadContext.LoadFromAssemblyPath([string]$built[$g.root])
 $exports = (Fence-Exports ($asm.GetType([string]$req.type,$true))) | ConvertFrom-Json -AsHashtable
 $receipt = @{key=$req.key;outputs=@($outputs);exports=@($exports)} | ConvertTo-Json -Depth 32 -Compress
 [IO.File]::WriteAllText([IO.Path]::Combine($dir,'receipt.json'),$receipt)
} catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
`
