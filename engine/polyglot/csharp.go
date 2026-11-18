package polyglot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// CSharp is a first-class C# fence compiled through PowerShell's Add-Type, so
// the only toolchain is the pinned PowerShell archive bashy provisions for the
// powershell fence (S358.2 proved Add-Type on Windows x64, macOS arm64 and
// Linux x64; no .NET SDK is involved). The fence body is `using` lines plus
// members: the body is wrapped in a static class in a namespace generated per
// module, because a loaded type cannot be redefined in a session. Its public
// static methods, discovered by reflection over the compiled assembly, become
// Bash# callables.
//
// The compiled assembly is cached under a key over the generated source and
// the content of the toolchain files that compile it, so an unchanged fence
// on an unchanged toolchain is analyzed from the cached export list and loaded
// from the cached assembly without compiling again. One persistent pwsh worker
// serves a module, speaking the PowerShell row's JSON-lines protocol.
type CSharp struct {
	Command     string
	Environment *EnvironmentPlan
	// Cwd, when set, answers the caller's directory at call time.
	Cwd func() string
	// CacheDir overrides the compiled-assembly cache directory.
	CacheDir string
}

func (c CSharp) powerShell() PowerShell {
	return PowerShell{Command: c.Command, Environment: c.Environment, Cwd: c.Cwd}
}

func (c CSharp) executable() string      { return c.powerShell().executable() }
func (c CSharp) name() string            { return "C#" }
func (c CSharp) configure(cmd *exec.Cmd) { c.powerShell().configure(cmd) }

func (c CSharp) arguments(Plan) []string {
	args := append(leadingArgs(c.Environment), powerShellBaseArgs()...)
	return append(args, csharpWorker)
}

// loadRequest names the cached assembly the worker loads, with the generated
// source it compiles from if the cache entry has gone since analysis.
func (c CSharp) loadRequest(plan Plan) map[string]any {
	return c.loadRequestContext(context.Background(), plan)
}

func (c CSharp) loadRequestContext(ctx context.Context, plan Plan) map[string]any {
	if graph := c.graphPayload(plan); graph != "" {
		u, _, err := c.ensureGraph(ctx, plan.Source, graph)
		if err != nil {
			return map[string]any{"id": 0, "op": "load", "error": err.Error()}
		}
		return map[string]any{"id": 0, "op": "load", "assembly": u.assembly, "type": u.typeName, "dependencies": u.dependencies, "graph": true}
	}
	unit, err := c.unit(plan.Source)
	if err != nil {
		return map[string]any{"id": 0, "op": "load", "error": err.Error()}
	}
	return map[string]any{"id": 0, "op": "load", "source": unit.source, "assembly": unit.assembly, "type": unit.typeName}
}

// csharpCompiles counts the compiler launches Analyze makes; a cache hit makes
// none.
var csharpCompiles atomic.Int64

// Analyze answers the fence's exports: from the cache when the same generated
// source was compiled by the same toolchain, otherwise by compiling it with
// Add-Type into the cache and reflecting over the assembly.
func (c CSharp) Analyze(ctx context.Context, source string) ([]Export, error) {
	if graph := c.graphPayload(Plan{}); graph != "" {
		_, exports, err := c.ensureGraph(ctx, source, graph)
		return exports, err
	}
	unit, err := c.unit(source)
	if err != nil {
		return nil, err
	}
	if exports, ok := unit.cachedExports(); ok {
		return exports, nil
	}
	request, err := json.Marshal(map[string]string{"source": unit.source, "assembly": unit.assembly, "type": unit.typeName})
	if err != nil {
		return nil, err
	}
	csharpCompiles.Add(1)
	name, argv := workerExecArgs(c.executable(), append(leadingArgs(c.Environment), append(powerShellBaseArgs(), csharpAnalyze)...))
	cmd := exec.CommandContext(ctx, name, argv...)
	c.configure(cmd)
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("C# runtime (pinned PowerShell) unavailable: %w", err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	out := bytes.TrimSpace(stdout.Bytes())
	var exports []Export
	if err := json.Unmarshal(out, &exports); err != nil {
		return nil, fmt.Errorf("invalid C# analyzer response: %w", err)
	}
	if len(exports) == 0 {
		return nil, errors.New("no public static C# methods to export")
	}
	unit.storeExports(out)
	return exports, nil
}

// csharpUnit is one fence's generated compilation unit and its cache entry.
type csharpUnit struct {
	source   string
	typeName string
	assembly string
	exports  string
}

func (c CSharp) unit(source string) (csharpUnit, error) {
	generated, typeName, err := csharpGenerate(source)
	if err != nil {
		return csharpUnit{}, err
	}
	toolchain, err := csharpToolchainHash(c.executable(), leadingArgs(c.Environment))
	if err != nil {
		return csharpUnit{}, fmt.Errorf("C# runtime (pinned PowerShell) unavailable: %w", err)
	}
	dir, err := c.cacheDir()
	if err != nil {
		return csharpUnit{}, err
	}
	sum := sha256.Sum256([]byte("bashpp-csharp-v2\x00" + runtime.GOOS + "/" + runtime.GOARCH + "\x00" + toolchain + "\x00" + generated))
	key := hex.EncodeToString(sum[:])
	return csharpUnit{
		source:   generated,
		typeName: typeName,
		assembly: filepath.Join(dir, key+".dll"),
		exports:  filepath.Join(dir, key+".json"),
	}, nil
}

// cachedExports answers the export list stored beside a compiled assembly. The
// list is written only after the assembly is in place, so its presence marks a
// complete entry.
func (u csharpUnit) cachedExports() ([]Export, bool) {
	data, err := os.ReadFile(u.exports)
	if err != nil {
		return nil, false
	}
	if info, err := os.Stat(u.assembly); err != nil || info.Size() == 0 {
		return nil, false
	}
	var exports []Export
	if json.Unmarshal(data, &exports) != nil || len(exports) == 0 {
		return nil, false
	}
	return exports, true
}

// storeExports records the export list atomically; a failure only costs the
// next run a compile.
func (u csharpUnit) storeExports(data []byte) {
	tmp, err := os.CreateTemp(filepath.Dir(u.exports), ".exports-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), u.exports) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// cacheDir is the first writable of: the explicit CacheDir, the user cache,
// and a per-user temp directory (a read-only image root still has a tmpfs).
func (c CSharp) cacheDir() (string, error) {
	var candidates []string
	if c.CacheDir != "" {
		candidates = []string{c.CacheDir}
	} else {
		if dir, err := os.UserCacheDir(); err == nil {
			candidates = append(candidates, filepath.Join(dir, "bashpp", "csharp"))
		}
		suffix := ""
		if uid := os.Getuid(); uid >= 0 {
			suffix = "-" + strconv.Itoa(uid)
		}
		candidates = append(candidates, filepath.Join(os.TempDir(), "bashpp-csharp"+suffix))
	}
	var last error
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			last = err
			continue
		}
		probe, err := os.CreateTemp(dir, ".probe-*")
		if err != nil {
			last = err
			continue
		}
		probe.Close()
		_ = os.Remove(probe.Name())
		return dir, nil
	}
	return "", fmt.Errorf("C# assembly cache unavailable: %w", last)
}

// csharpToolchainFiles are the files beside pwsh that decide what Add-Type
// compiles: the PowerShell engine and the Roslyn compiler it hosts.
var csharpToolchainFiles = []string{"System.Management.Automation.dll", "Microsoft.CodeAnalysis.dll", "Microsoft.CodeAnalysis.CSharp.dll"}

var csharpToolchainMemo sync.Map // stamp -> content hash

// csharpToolchainHash is a content hash of the pwsh launcher and the compiler
// assemblies beside it (and beside a launcher argument naming a file, the
// framework-dependent `dotnet pwsh.dll` form). The hash is memoized per
// process by path, size and modification time.
func csharpToolchainHash(executable string, args []string) (string, error) {
	path := executable
	if !filepath.IsAbs(path) {
		found, err := exec.LookPath(path)
		if err != nil {
			return "", err
		}
		path = found
	}
	var files []string
	for _, candidate := range append([]string{path}, args...) {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if candidate == path {
				return "", err
			}
			continue
		}
		if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, resolved)
		for _, name := range csharpToolchainFiles {
			if file := filepath.Join(filepath.Dir(resolved), name); fileExists(file) {
				files = append(files, file)
			}
		}
	}
	var stamp strings.Builder
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00", file, info.Size(), info.ModTime().UnixNano())
	}
	if hash, ok := csharpToolchainMemo.Load(stamp.String()); ok {
		return hash.(string), nil
	}
	h := sha256.New()
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	hash := hex.EncodeToString(h.Sum(nil))
	csharpToolchainMemo.Store(stamp.String(), hash)
	return hash, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

var (
	csharpLineDirective  = regexp.MustCompile(`^#line ([0-9]+) ("(?:[^"\\]|\\.)*")$`)
	csharpUsingDirective = regexp.MustCompile(`^(global\s+)?using\s+[^\s(][^;(]*;\s*(//.*)?$`)
)

const csharpBodyMarkerPrefix = "// <bashpp-csharp-body-v2-"

func csharpBodyMarker(typeName string) string {
	return csharpBodyMarkerPrefix + typeName + ">"
}

// CSharpNuGetFollowUp names the separate story that owns NuGet packages; a
// fence declaring a package is refused with it.
const CSharpNuGetFollowUp = "S358.6 (7455eb7e99dd)"

type csharpLine struct {
	text string
	file string
	line int
}

// csharpGenerate separates an aggregated fence's leading using directives
// from its body and answers the generated type that holds the exports. The
// compile helper uses the marker to partition compilation-unit declarations
// with Roslyn before wrapping the residual members in that type. Every emitted
// source line keeps a #line mapping to its fence line.
func csharpGenerate(source string) (string, string, error) {
	file, line := "<bash++ csharp>", 1
	leading := true
	var header, body []csharpLine
	for _, text := range strings.Split(strings.TrimSuffix(source, "\n"), "\n") {
		text = strings.TrimSuffix(text, "\r")
		if m := csharpLineDirective.FindStringSubmatch(text); m != nil {
			if name, err := strconv.Unquote(m[2]); err == nil {
				n, _ := strconv.Atoi(m[1])
				file, line, leading = name, n, true
				continue
			}
		}
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "#:") || strings.HasPrefix(trimmed, "#r ") || strings.HasPrefix(trimmed, "#r\t") {
			return "", "", fmt.Errorf("%s:%d: C# fence package declaration %q is not supported; NuGet packages are the separate follow-up %s", file, line, trimmed, CSharpNuGetFollowUp)
		}
		entry := csharpLine{text: text, file: file, line: line}
		if leading && (trimmed == "" || strings.HasPrefix(trimmed, "//") || csharpUsingDirective.MatchString(trimmed)) {
			header = append(header, entry)
		} else {
			leading = false
			body = append(body, entry)
		}
		line++
	}
	sum := sha256.Sum256([]byte(source))
	namespace := "BashPP.CSharp.M" + hex.EncodeToString(sum[:8])
	var out strings.Builder
	emit := func(lines []csharpLine) {
		prev := csharpLine{line: -1}
		for _, l := range lines {
			if l.file != prev.file || l.line != prev.line+1 {
				fmt.Fprintf(&out, "#line %d \"%s\"\n", l.line, csharpLineFile(l.file))
			}
			out.WriteString(l.text)
			out.WriteByte('\n')
			prev = l
		}
	}
	emit(header)
	out.WriteString(csharpBodyMarker(namespace + ".Fence"))
	out.WriteByte('\n')
	emit(body)
	return out.String(), namespace + ".Fence", nil
}

// csharpLineFile is a file name as a C# #line directive takes it: verbatim
// (no escapes), so a quote, the only character it cannot hold, is replaced.
func csharpLineFile(name string) string {
	return strings.ReplaceAll(name, `"`, "'")
}

// csharpCompilePrelude compiles a generated unit with Add-Type into a cache
// path and maps a compiled type's public static methods to the Bash# export
// shape. A compile goes to a private temporary directory and is renamed into
// place only on success, so a failed compile never leaves a cache entry, and a
// concurrent compile of the same key that wins the rename is equally good.
// Compiler errors are reported with the mapped (#line) fence locations.
const csharpCompilePrelude = `
function Test-FenceExtensionType($member) {
    if (-not ($member -is [Microsoft.CodeAnalysis.CSharp.Syntax.TypeDeclarationSyntax])) { return $false }
    $isStatic = $false
    foreach ($modifier in $member.Modifiers) {
        if ($modifier.RawKind -eq [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::StaticKeyword) { $isStatic = $true; break }
    }
    if (-not $isStatic) { return $false }
    foreach ($method in $member.Members) {
        if (-not ($method -is [Microsoft.CodeAnalysis.CSharp.Syntax.MethodDeclarationSyntax]) -or $method.ParameterList.Parameters.Count -eq 0) { continue }
        foreach ($modifier in $method.ParameterList.Parameters[0].Modifiers) {
            if ($modifier.RawKind -eq [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::ThisKeyword) { return $true }
        }
    }
    return $false
}
function Test-FenceUnitDeclaration($member) {
    return ($member -is [Microsoft.CodeAnalysis.CSharp.Syntax.BaseNamespaceDeclarationSyntax] -or
        $member -is [Microsoft.CodeAnalysis.CSharp.Syntax.BaseTypeDeclarationSyntax] -or
        $member -is [Microsoft.CodeAnalysis.CSharp.Syntax.DelegateDeclarationSyntax])
}
function Test-FenceFileLocalType($member) {
    if (-not ($member -is [Microsoft.CodeAnalysis.CSharp.Syntax.BaseTypeDeclarationSyntax])) { return $false }
    foreach ($modifier in $member.Modifiers) {
        if ($modifier.RawKind -eq [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::FileKeyword) { return $true }
    }
    return $false
}
function Test-FenceUsingStaticLocalType($root, [string]$header) {
    # csharpGenerate lifts leading using directives ahead of the body marker.
    # Search both syntax roots: the authored type remains in the body root.
    $headerRoot = [Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText($header).GetCompilationUnitRoot()
    foreach ($using in @($headerRoot.Usings) + @($root.Usings)) {
        if ($using.StaticKeyword.IsMissing -or $using.StaticKeyword.RawKind -ne [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::StaticKeyword) { continue }
        if (-not ($using.Name -is [Microsoft.CodeAnalysis.CSharp.Syntax.IdentifierNameSyntax])) { continue }
        $name = $using.Name.Identifier.ValueText
        foreach ($member in $root.Members) {
            if ($member -is [Microsoft.CodeAnalysis.CSharp.Syntax.BaseTypeDeclarationSyntax] -and $member.Identifier.ValueText -ceq $name) { return $true }
        }
    }
    return $false
}
function Find-FenceFileNamespaceResidual($member) {
    # In a file-scoped namespace, a free fence method after a type is not a
    # legal compilation-unit member. Roslyn recovers by attaching the method
    # to that type and preserving the type's real closing brace as skipped
    # trivia on the method. Use that structured recovery token as the split;
    # plain text or brace counting would misread comments, strings, and raw
    # strings.
    foreach ($node in $member.DescendantNodesAndSelf()) {
        foreach ($trivia in $node.GetLeadingTrivia()) {
            if (-not $trivia.HasStructure -or $trivia.RawKind -ne [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::SkippedTokensTrivia) { continue }
            foreach ($token in $trivia.GetStructure().ChildTokens()) {
                if ($token.RawKind -eq [int][Microsoft.CodeAnalysis.CSharp.SyntaxKind]::CloseBraceToken) {
                    return [pscustomobject]@{ CloseAt = $token.Span.End; Node = $node }
                }
            }
        }
    }
    return $null
}
function Normalize-FenceSource([string]$src, [string]$typeName) {
    # The generated type is derived from the original source hash. Including
    # it in the delimiter prevents user comments containing the old fixed
    # marker text from being mistaken for the header/body boundary.
    $marker = '// <bashpp-csharp-body-v2-' + $typeName + '>'
    $at = $src.IndexOf($marker, [System.StringComparison]::Ordinal)
    if ($at -lt 0) { throw 'C# fence source is missing its body marker' }
    $header = $src.Substring(0, $at)
    $bodyAt = $at + $marker.Length
    if ($bodyAt -lt $src.Length -and $src[$bodyAt] -eq [char]13) { $bodyAt++ }
    if ($bodyAt -lt $src.Length -and $src[$bodyAt] -eq [char]10) { $bodyAt++ }
    $body = $src.Substring($bodyAt)
    $root = [Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText($body).GetCompilationUnitRoot()
    $fileScoped = @($root.Members | Where-Object { $_ -is [Microsoft.CodeAnalysis.CSharp.Syntax.FileScopedNamespaceDeclarationSyntax] })
    if ($fileScoped.Count -gt 1) { throw 'C# fence has more than one file-scoped namespace in one source block' }
    $fileNamespaceScope = ''
    if ($fileScoped.Count -eq 1) {
        $fileNamespace = $fileScoped[0]
        $closeAt = $fileNamespace.Span.End
        $residualNode = $null
        $sawResidual = $false
        foreach ($member in $fileNamespace.Members) {
            if (Test-FenceUnitDeclaration $member) {
                if ($sawResidual) { throw 'C# fence file-scoped namespace interleaves exported members and type declarations' }
                $recovered = Find-FenceFileNamespaceResidual $member
                if ($null -ne $recovered) {
                    $closeAt = $recovered.CloseAt
                    $residualNode = $recovered.Node
                    $sawResidual = $true
                }
                continue
            }
            if (-not $sawResidual) {
                $closeAt = $member.FullSpan.Start
                $residualNode = $member
                $sawResidual = $true
            }
        }
        $fileNamespaceScope = 'using global::' + $fileNamespace.Name.ToString() + ';' + [char]10
        foreach ($using in $fileNamespace.Usings) {
            $fileNamespaceScope += $using.ToString() + [char]10
        }
        $semi = $fileNamespace.SemicolonToken.Span
        $lf = [char]10
        $residualAt = $closeAt
        $lineReset = ''
        if ($sawResidual) {
            $resetLine = $residualNode.GetLocation().GetMappedLineSpan()
            $column = $residualNode.GetLocation().GetLineSpan().StartLinePosition.Character
            $residualAt = $residualNode.Span.Start - $column
            if ($resetLine.IsValid -and $resetLine.Path) {
                $path = $resetLine.Path.Replace('"', "'")
                $lineReset = '#line ' + ($resetLine.StartLinePosition.Line + 1) + ' "' + $path + '"' + $lf
            }
        }
        $body = $body.Substring(0, $semi.Start) + ' {' +
            $body.Substring($semi.End, $closeAt - $semi.End) + $lf + '}' + $lf +
            $body.Substring($closeAt, $residualAt - $closeAt) + $lineReset + $body.Substring($residualAt)
        $root = [Microsoft.CodeAnalysis.CSharp.CSharpSyntaxTree]::ParseText($body).GetCompilationUnitRoot()
    }
    $unitMode = Test-FenceUsingStaticLocalType $root $header
    foreach ($member in $root.Members) {
        if ($member -is [Microsoft.CodeAnalysis.CSharp.Syntax.BaseNamespaceDeclarationSyntax] -or
            (Test-FenceExtensionType $member) -or (Test-FenceFileLocalType $member)) {
            $unitMode = $true
        }
    }
    $dot = $typeName.LastIndexOf('.')
    if ($dot -le 0 -or $typeName.Substring($dot + 1) -ne 'Fence') { throw ('invalid generated C# fence type ' + $typeName) }
    $namespace = $typeName.Substring(0, $dot)
    $lf = [char]10
    if (-not $unitMode) {
        return $header + 'namespace ' + $namespace + ' { public static class Fence {' + $lf + $body + $lf + '#line hidden' + $lf + '} }' + $lf
    }
    $nodes = @()
    foreach ($attribute in $root.AttributeLists) { $nodes += $attribute }
    foreach ($using in $root.Usings) { $nodes += $using }
    foreach ($member in $root.Members) { $nodes += $member }
    $nodes = @($nodes | Sort-Object { $_.FullSpan.Start })
    $out = New-Object System.Text.StringBuilder
    [void]$out.Append($header)
    $cursor = 0
    foreach ($node in $nodes) {
        $full = $node.FullSpan
        if ($full.Start -gt $cursor) { [void]$out.Append($body.Substring($cursor, $full.Start - $cursor)) }
        if ($node -is [Microsoft.CodeAnalysis.CSharp.Syntax.AttributeListSyntax] -or
            $node -is [Microsoft.CodeAnalysis.CSharp.Syntax.UsingDirectiveSyntax] -or
            (Test-FenceUnitDeclaration $node)) {
            [void]$out.Append($body.Substring($full.Start, $full.Length))
        } else {
            $span = $node.Span
            [void]$out.Append($body.Substring($full.Start, $span.Start - $full.Start))
            [void]$out.Append('namespace ' + $namespace + ' {' + $lf + $fileNamespaceScope + 'public static partial class Fence {' + $lf)
            $mapped = $node.GetLocation().GetMappedLineSpan()
            if ($mapped.IsValid -and $mapped.Path) {
                $path = $mapped.Path.Replace('"', "'")
                [void]$out.Append('#line ' + ($mapped.StartLinePosition.Line + 1) + ' "' + $path + '"' + $lf)
            }
            $column = $node.GetLocation().GetLineSpan().StartLinePosition.Character
            if ($column -gt 0) { [void]$out.Append(''.PadLeft($column)) }
            [void]$out.Append($body.Substring($span.Start, $span.Length))
            [void]$out.Append($lf + '#line hidden' + $lf + '} }' + $lf)
            [void]$out.Append($body.Substring($span.End, $full.End - $span.End))
        }
        $cursor = $full.End
    }
    if ($cursor -lt $body.Length) { [void]$out.Append($body.Substring($cursor)) }
    return $out.ToString()
}
function Compile-Fence([string]$src, [string]$assembly, [string]$typeName) {
    $dir = [System.IO.Path]::GetDirectoryName($assembly)
    $tmpDir = [System.IO.Path]::Combine($dir, '.tmp-' + $PID + '-' + [guid]::NewGuid().ToString('N'))
    [void][System.IO.Directory]::CreateDirectory($tmpDir)
    try {
        $tmp = [System.IO.Path]::Combine($tmpDir, [System.IO.Path]::GetFileName($assembly))
        $diag = $null
        $failure = $null
        try {
            $normalized = Normalize-FenceSource $src $typeName
            Add-Type -TypeDefinition $normalized -Language CSharp -CompilerOptions '/unsafe' -OutputAssembly $tmp -OutputType Library -IgnoreWarnings -ErrorAction SilentlyContinue -ErrorVariable diag -WarningAction SilentlyContinue
        } catch {
            $failure = $_
        }
        $lines = @()
        foreach ($e in @($diag)) {
            $d = $e.TargetObject
            if ($d -is [Microsoft.CodeAnalysis.Diagnostic] -and $d.Severity -eq [Microsoft.CodeAnalysis.DiagnosticSeverity]::Error) {
                $s = $d.Location.GetMappedLineSpan()
                $lines += ('{0}:{1}:{2}: error {3}: {4}' -f $s.Path, ($s.StartLinePosition.Line + 1), ($s.StartLinePosition.Character + 1), $d.Id, $d.GetMessage([System.Globalization.CultureInfo]::InvariantCulture))
            }
        }
        if ($lines.Count -gt 0) { throw ($lines -join "` + "`n" + `") }
        if ($failure) { throw ('C# compilation failed: ' + $failure.Exception.Message) }
        if (-not [System.IO.File]::Exists($tmp) -or (New-Object System.IO.FileInfo($tmp)).Length -eq 0) { throw 'C# compilation produced no assembly' }
        try {
            [System.IO.File]::Move($tmp, $assembly)
        } catch {
            if (-not [System.IO.File]::Exists($assembly)) { throw }
        }
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
function MapCSharpType([type]$t) {
    $u = [System.Nullable]::GetUnderlyingType($t)
    if ($null -ne $u) { $t = $u }
    switch ($t.FullName) {
        'System.String' { return 'string' }
        'System.Char' { return 'string' }
        'System.Boolean' { return 'bool' }
        'System.SByte' { return 'int' }
        'System.Byte' { return 'int' }
        'System.Int16' { return 'int' }
        'System.UInt16' { return 'int' }
        'System.Int32' { return 'int' }
        'System.UInt32' { return 'int' }
        'System.Int64' { return 'int' }
        'System.UInt64' { return 'int' }
        'System.Numerics.BigInteger' { return 'int' }
        'System.Single' { return 'float64' }
        'System.Double' { return 'float64' }
        'System.Decimal' { return 'float64' }
        'System.Byte[]' { return 'bytes' }
        'System.Void' { return 'nil' }
        'System.Object' { return 'any' }
        default { return 'object' }
    }
}
function CSharpExportFlags { return [System.Reflection.BindingFlags]'Public,Static,DeclaredOnly' }
# Fence-Exports lists the generated type's public static methods, in
# declaration order, as the Bash# export JSON. A name is one callable, so an
# overload, a generic method or a ref/out parameter is refused at prepare.
function Fence-Exports([type]$type) {
    $methods = @($type.GetMethods((CSharpExportFlags)) | Where-Object { -not $_.IsSpecialName -and -not $_.Name.StartsWith('_') } | Sort-Object MetadataToken)
    $seen = @{}
    $parts = @()
    foreach ($m in $methods) {
        $name = $m.Name
        if ($seen.ContainsKey($name)) { throw ('C# method ' + $name + ' is overloaded; a fence export has one signature') }
        $seen[$name] = $true
        if ($m.IsGenericMethodDefinition) { throw ('C# method ' + $name + ' is generic; a fence export needs concrete parameter types') }
        $dynamic = $false
        $params = @()
        foreach ($p in $m.GetParameters()) {
            if ($p.ParameterType.IsByRef) { throw ('C# method ' + $name + ' has a ref/out parameter ' + $p.Name + '; a fence export takes values') }
            $kind = MapCSharpType $p.ParameterType
            if ($kind -eq 'any') { $dynamic = $true }
            $params += $kind
        }
        $rk = MapCSharpType $m.ReturnType
        $results = @($rk)
        if ($rk -eq 'nil') { $results = @() }
        elseif ($rk -eq 'any') { $dynamic = $true }
        $pstr = '[' + (($params | ForEach-Object { ConvertTo-Json -InputObject ([string]$_) -Compress }) -join ',') + ']'
        $rstr = '[' + (($results | ForEach-Object { ConvertTo-Json -InputObject ([string]$_) -Compress }) -join ',') + ']'
        $d = 'false'
        if ($dynamic) { $d = 'true' }
        $parts += '{"name":' + (ConvertTo-Json -InputObject $name -Compress) + ',"signature":{"params":' + $pstr + ',"results":' + $rstr + ',"dynamic":' + $d + '}}'
    }
    return '[' + ($parts -join ',') + ']'
}
`

// csharpAnalyze reads {source, assembly, type} from stdin, compiles the unit
// into the cache unless a concurrent run already has, and writes the export
// JSON to stdout; a failure is its message on stderr and status 1.
const csharpAnalyze = csharpCompilePrelude + `
$ErrorActionPreference = 'Stop'
try {
    $req = [Console]::In.ReadToEnd() | ConvertFrom-Json -AsHashtable
    $assembly = [string]$req['assembly']
    if (-not [System.IO.File]::Exists($assembly)) { Compile-Fence ([string]$req['source']) $assembly ([string]$req['type']) }
    $asm = [System.Reflection.Assembly]::LoadFrom($assembly)
    $type = $asm.GetType([string]$req['type'], $true)
    [Console]::Out.Write((Fence-Exports $type))
} catch {
    [Console]::Error.Write($_.Exception.Message)
    exit 1
}
`

// csharpWorker is the persistent C# worker: the PowerShell row's protocol and
// value codec, a load that brings the cached assembly into the session
// (compiling it first if the cache entry has gone), and a call that converts
// each argument to the declared parameter type and invokes the static method.
// Console output written during a call is that call's stdout and stderr; an
// exception the method throws is the call's error, with its type and message.
const csharpWorker = powershellWorkerPrelude + csharpCompilePrelude + `
$script:fenceType = $null
function ConvertTo-Parameter($value, [type]$type) {
    if ($null -eq $value) {
        if ($type.IsValueType -and $null -eq [System.Nullable]::GetUnderlyingType($type)) { throw ('null passed for a ' + $type.Name + ' parameter') }
        return $null
    }
    # The leading comma keeps an array (byte[], long[]) one value: a function's
    # output would otherwise enumerate it.
    if ($type -eq [object]) { return ,$value }
    return ,[System.Management.Automation.LanguagePrimitives]::ConvertTo($value, $type)
}
function CSharp-Error($failure) {
    $ex = $failure.Exception
    while (($ex -is [System.Management.Automation.MethodInvocationException] -or $ex -is [System.Reflection.TargetInvocationException]) -and $null -ne $ex.InnerException) {
        $ex = $ex.InnerException
    }
    return @{ code = $ex.GetType().Name; message = $ex.Message; help = '' }
}

while ($true) {
    $line = $stdinReader.ReadLine()
    if ($null -eq $line) { break }
    if ($line.Trim() -eq '') { continue }
    $rid = 0
    $res = $null
    try {
        $req = ConvertFrom-Json -InputObject $line -AsHashtable
        if ($req.ContainsKey('id')) { $rid = [int64]$req['id'] }
        $op = [string]$req['op']
        if ($req.ContainsKey('cwd') -and $req['cwd'] -and $op -ne 'job_join' -and $op -ne 'job_leave') {
            Set-Location -LiteralPath ([string]$req['cwd'])
            [System.Environment]::CurrentDirectory = (Get-Location -PSProvider FileSystem).ProviderPath
        }
        switch ($op) {
            'job_join' { $res = @{ id = $rid; ok = $true; result = $PID; stdout = ''; stderr = '' } }
            'job_leave' { $res = @{ id = $rid; ok = $true; result = $PID; stdout = ''; stderr = '' } }
            'load' {
                if ($req.ContainsKey('error') -and $req['error']) { throw [string]$req['error'] }
                $assembly = [string]$req['assembly']
                $compiled = $false
                if ($req['graph']) {
                    if (-not [IO.File]::Exists($assembly)) { throw 'C# graph cache disappeared before load' }
                    $script:graphContext = [System.Runtime.Loader.AssemblyLoadContext]::new(('fence-' + [guid]::NewGuid().ToString('N')), $false)
                    foreach ($dependency in $req.dependencies) { [void]$script:graphContext.LoadFromAssemblyPath([string]$dependency) }
                }
                if (-not [System.IO.File]::Exists($assembly)) { Compile-Fence ([string]$req['source']) $assembly ([string]$req['type']); $compiled = $true }
                if ($req['graph']) { $script:fenceAssembly = $script:graphContext.LoadFromAssemblyPath($assembly) }
                else { $script:fenceAssembly = [System.Reflection.Assembly]::LoadFrom($assembly) }
                $script:fenceType = $script:fenceAssembly.GetType([string]$req['type'], $true)
                $res = @{ id = $rid; ok = $true; result = @{ compiled = $compiled }; stdout = ''; stderr = '' }
            }
            'call' {
                $fname = [string]$req['name']
                if ($req.ContainsKey('kwargs') -and $req['kwargs'] -is [System.Collections.IDictionary] -and $req['kwargs'].Count -gt 0) {
                    throw [System.ArgumentException]::new('C# methods do not accept named arguments')
                }
                $method = $null
                foreach ($m in $script:fenceType.GetMethods((CSharpExportFlags))) {
                    if ($m.Name -ceq $fname -and -not $m.IsSpecialName) { $method = $m; break }
                }
                if ($null -eq $method) { throw [System.MissingMethodException]::new('no C# method ' + $fname) }
                $argv = @()
                if ($req.ContainsKey('args') -and $null -ne $req['args']) {
                    foreach ($a in @($req['args'])) { $argv += ,(Dec $a) }
                }
                $params = $method.GetParameters()
                if ($argv.Count -gt $params.Count) { throw [System.ArgumentException]::new('C# method ' + $fname + ' takes ' + $params.Count + ' arguments, got ' + $argv.Count) }
                $converted = New-Object object[] $params.Count
                for ($i = 0; $i -lt $params.Count; $i++) {
                    if ($i -lt $argv.Count) { $converted[$i] = ConvertTo-Parameter $argv[$i] $params[$i].ParameterType }
                    elseif ($params[$i].HasDefaultValue) { $converted[$i] = $params[$i].DefaultValue }
                    else { throw [System.ArgumentException]::new('C# method ' + $fname + ' takes ' + $params.Count + ' arguments, got ' + $argv.Count) }
                }
                $outWriter = New-Object System.IO.StringWriter
                $errWriter = New-Object System.IO.StringWriter
                $savedOut = [Console]::Out
                $savedErr = [Console]::Error
                $value = $null
                $failure = $null
                [Console]::SetOut($outWriter)
                [Console]::SetError($errWriter)
                try {
                    $value = $method.Invoke($null, $converted)
                } catch {
                    $failure = $_
                } finally {
                    [Console]::SetOut($savedOut)
                    [Console]::SetError($savedErr)
                }
                $o = Normalize-Newlines $outWriter.ToString()
                $e = Normalize-Newlines $errWriter.ToString()
                if ($failure) {
                    $res = @{ id = $rid; ok = $false; error = (CSharp-Error $failure); stdout = $o; stderr = $e }
                } else {
                    $res = @{ id = $rid; ok = $true; result = (Enc $value); stdout = $o; stderr = $e }
                }
            }
            'getattr' {
                $name = [string]$req['name']
                $target = $null
                if ($req.ContainsKey('handle')) {
                    $hid = [int64]$req['handle']
                    if (-not $script:handles.ContainsKey($hid)) { throw ('stale C# handle ' + $hid) }
                    $target = $script:handles[$hid]
                }
                $res = @{ id = $rid; ok = $true; result = (Enc $target.$name); stdout = ''; stderr = '' }
            }
            'release' {
                if ($req.ContainsKey('handle')) { $script:handles.Remove([int64]$req['handle']) | Out-Null }
                $res = @{ id = $rid; ok = $true; result = $null; stdout = ''; stderr = '' }
            }
            default { throw ('unknown operation ' + $op) }
        }
    } catch {
        $res = @{ id = $rid; ok = $false; error = (Envelope-Error $_); stdout = ''; stderr = '' }
    }
    Write-Protocol $res
}
`
