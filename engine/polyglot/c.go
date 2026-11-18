package polyglot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// C and CPP analyze declaration-only native fences and compile them into the
// same self-contained call-worker artifact used by Rust fences. The compiler
// remains an external process; Bash++ and the sh module stay pure Go.
type C struct {
	Command     string
	Environment *EnvironmentPlan
}

type CPP struct {
	Command     string
	Environment *EnvironmentPlan
}

func (c C) executable() string              { return nativeCompiler(c.Command, c.Environment, "clang") }
func (c CPP) executable() string            { return nativeCompiler(c.Command, c.Environment, "clang++") }
func (C) arguments(Plan) []string           { return nil }
func (CPP) arguments(Plan) []string         { return nil }
func (C) loadRequest(Plan) map[string]any   { return nil }
func (CPP) loadRequest(Plan) map[string]any { return nil }
func (C) name() string                      { return "C" }
func (CPP) name() string                    { return "C++" }
func (c C) configure(cmd *exec.Cmd)         { configureNativeCompiler(cmd, c.Environment) }
func (c CPP) configure(cmd *exec.Cmd)       { configureNativeCompiler(cmd, c.Environment) }

func nativeCompiler(command string, environment *EnvironmentPlan, fallback string) string {
	if environment != nil && environment.Executable != "" {
		return environment.Executable
	}
	if command != "" {
		return command
	}
	return fallback
}

func configureNativeCompiler(cmd *exec.Cmd, environment *EnvironmentPlan) {
	if environment == nil {
		return
	}
	cmd.Dir = environment.Dir
	cmd.Env = append([]string(nil), environment.Env...)
}

type clangASTNode struct {
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	StorageClass string         `json:"storageClass"`
	Type         clangASTType   `json:"type"`
	Loc          clangASTLoc    `json:"loc"`
	Range        clangASTRange  `json:"range"`
	Inner        []clangASTNode `json:"inner"`
}

type clangASTType struct {
	QualType string `json:"qualType"`
}
type clangASTRange struct{ Begin, End clangASTLoc }
type clangASTLoc struct {
	Offset       int          `json:"offset"`
	File         string       `json:"file"`
	IncludedFrom *clangASTLoc `json:"includedFrom"`
}

type nativeExport struct {
	Export
	params []string
	result string
}

func (c C) Analyze(ctx context.Context, source string) ([]Export, error) {
	exports, _, err := c.AnalyzeArtifact(ctx, source)
	return exports, err
}

func (c CPP) Analyze(ctx context.Context, source string) ([]Export, error) {
	exports, _, err := c.AnalyzeArtifact(ctx, source)
	return exports, err
}

func (c C) AnalyzeArtifact(ctx context.Context, source string) ([]Export, string, error) {
	return analyzeNativeArtifact(ctx, "c", c.executable(), c.Environment, source)
}

func (c CPP) AnalyzeArtifact(ctx context.Context, source string) ([]Export, string, error) {
	return analyzeNativeArtifact(ctx, "cpp", c.executable(), c.Environment, source)
}

func analyzeNativeArtifact(ctx context.Context, language, compiler string, environment *EnvironmentPlan, source string) ([]Export, string, error) {
	dir, err := os.MkdirTemp("", "bashpp-"+language+"-build-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	ext := ".c"
	standard := "-std=c17"
	if language == "cpp" {
		ext, standard = ".cpp", "-std=c++20"
	}
	sourceFile := filepath.Join(dir, "module"+ext)
	if err := os.WriteFile(sourceFile, []byte(source), 0o600); err != nil {
		return nil, "", err
	}
	hostInclude, err := nativeHostIncludeArgs(runtime.GOOS, compiler, environment)
	if err != nil {
		return nil, "", err
	}
	var moduleArgs, moduleObjects []string
	if language == "cpp" {
		moduleArgs, moduleObjects, err = buildNativeModules(ctx, compiler, environment, dir, hostInclude)
		if err != nil {
			return nil, "", err
		}
	}
	// -o pins the output zig's driver insists on writing even under
	// -fsyntax-only (it would otherwise drop an a.out into the project dir).
	astArgs := append(leadingArgs(environment), standard, "-Xclang", "-ast-dump=json", "-fsyntax-only", "-o", filepath.Join(dir, "syntax.o"))
	astArgs = append(astArgs, nativeIncludeArgs(environment)...)
	astArgs = append(astArgs, hostInclude...)
	astArgs = append(astArgs, moduleArgs...)
	astArgs = append(astArgs, sourceFile)
	cmd := exec.CommandContext(ctx, compiler, astArgs...)
	configureNativeCompiler(cmd, environment)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil && !zigDriverOnlyFailure(stderr.String()) {
		return nil, "", nativeCompilerError(language, err, stderr.String())
	}
	var root clangASTNode
	if err := json.Unmarshal(stdout.Bytes(), &root); err != nil {
		return nil, "", fmt.Errorf("invalid Clang AST response: %w", err)
	}
	parsed, err := nativeExports(root, sourceFile, source, language)
	if err != nil {
		return nil, "", err
	}
	if len(parsed) == 0 {
		return nil, "", fmt.Errorf("no supported public %s functions", nativeLanguageName(language))
	}
	generated, err := nativeWorkerSource(language, source, parsed)
	if err != nil {
		return nil, "", err
	}
	workerFile := filepath.Join(dir, "worker"+ext)
	if err := os.WriteFile(workerFile, []byte(generated), 0o600); err != nil {
		return nil, "", err
	}
	output := filepath.Join(dir, "module")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	args := append(leadingArgs(environment), standard, "-O0", "-o", output)
	args = append(args, nativeIncludeArgs(environment)...)
	args = append(args, hostInclude...)
	args = append(args, moduleArgs...)
	args = append(args, workerFile)
	args = append(args, moduleObjects...)
	cmd = exec.CommandContext(ctx, compiler, args...)
	configureNativeCompiler(cmd, environment)
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", nativeCompilerError(language, err, stderr.String())
	}
	binary, err := os.ReadFile(output)
	if err != nil {
		return nil, "", err
	}
	exports := make([]Export, len(parsed))
	for i := range parsed {
		exports[i] = parsed[i].Export
	}
	return exports, string(binary), nil
}

// nativeIncludeArgs makes the fence's source directory and the discovered
// project root include roots for quoted includes only (-iquote). A plain -I
// would also serve angle-bracket includes, so a project file named like a
// standard header — tesseract's VERSION on a case-insensitive filesystem
// shadowing C++20's <version>, or any root file called version, string, map
// — would break every fence that includes the standard library.
func nativeIncludeArgs(environment *EnvironmentPlan) []string {
	if environment == nil {
		return nil
	}
	seen := map[string]bool{}
	var args []string
	for _, dir := range []string{environment.SourceDir, environment.Root} {
		if dir != "" && !seen[dir] {
			args, seen[dir] = append(args, "-iquote", dir), true
		}
	}
	return args
}

// zigDriverOnlyFailure recognises zig cc's one spurious failure: clang behind
// zig's driver dumps the AST, then zig reports "<file>:1:1: error:
// FileNotFound" because -fsyntax-only left it no object to cache. The dump
// on stdout is complete and the exit status is noise — but only when that
// is the SOLE error line; any clang diagnostic keeps the failure.
func zigDriverOnlyFailure(stderr string) bool {
	sawZig := false
	for _, line := range strings.Split(stderr, "\n") {
		// A diagnostic is "<loc>: error: <msg>"; a bare "error:" substring
		// also matches an include path such as libcxx's system_error:152:.
		if !strings.Contains(line, ": error: ") && !strings.HasPrefix(line, "error: ") {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ":1:1: error: FileNotFound") {
			sawZig = true
			continue
		}
		return false
	}
	return sawZig
}

func nativeCompilerError(language string, err error, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s compiler unavailable: %w", nativeLanguageName(language), err)
	}
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
}

func nativeLanguageName(language string) string {
	if language == "cpp" {
		return "C++"
	}
	return "C"
}

func nativeExports(root clangASTNode, sourceFile, source string, language string) ([]nativeExport, error) {
	var out []nativeExport
	seen := map[string]bool{}
	files := nativeSourceFiles(sourceFile, source)
	currentUserFile := true
	for _, node := range root.Inner {
		if node.Loc.File != "" {
			currentUserFile = files[filepath.Clean(node.Loc.File)]
		}
		userNode := currentUserFile && node.Loc.IncludedFrom == nil && node.Range.Begin.IncludedFrom == nil && node.Loc.Offset >= 0 && node.Loc.Offset <= len(source)
		if userNode {
			switch node.Kind {
			case "VarDecl":
				// C variables are declarations within the translation unit, not
				// callable exports. Keep C++'s existing initialization boundary.
				if language != "c" {
					return nil, fmt.Errorf("%s source fences do not allow top-level variables", nativeLanguageName(language))
				}
			case "FunctionTemplateDecl", "ClassTemplateDecl":
				return nil, fmt.Errorf("%s source fences do not export %s declarations", nativeLanguageName(language), node.Kind)
			}
		}
		if node.Kind != "FunctionDecl" || node.StorageClass == "static" || !nativeFunctionDefinition(node) || !userNode {
			continue
		}
		if node.Name == "" || strings.HasPrefix(node.Name, "__bpp_") {
			continue
		}
		if strings.Contains(node.Type.QualType, "...") {
			return nil, fmt.Errorf("%s function %s is variadic", nativeLanguageName(language), node.Name)
		}
		if seen[node.Name] {
			return nil, fmt.Errorf("%s function %s is overloaded or exported more than once", nativeLanguageName(language), node.Name)
		}
		seen[node.Name] = true
		var params, bridge []string
		for _, child := range node.Inner {
			if child.Kind != "ParmVarDecl" {
				continue
			}
			typ := normalizeNativeType(child.Type.QualType)
			mapped, ok := nativeBridgeType(language, typ, false)
			if !ok {
				return nil, fmt.Errorf("%s function %s has unsupported parameter type %s", nativeLanguageName(language), node.Name, typ)
			}
			params, bridge = append(params, typ), append(bridge, mapped)
		}
		result := nativeResultType(node.Type.QualType)
		mapped, ok := nativeBridgeType(language, result, true)
		if !ok {
			return nil, fmt.Errorf("%s function %s has unsupported result type %s", nativeLanguageName(language), node.Name, result)
		}
		sig := Signature{Params: bridge}
		if mapped != "nil" {
			sig.Results = []string{mapped}
		}
		out = append(out, nativeExport{Export: Export{Name: node.Name, Signature: sig}, params: params, result: result})
	}
	return out, nil
}

func nativeSourceFiles(sourceFile, source string) map[string]bool {
	files := map[string]bool{filepath.Clean(sourceFile): true}
	for _, line := range strings.Split(source, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "#line" {
			continue
		}
		if name, err := strconv.Unquote(strings.Join(fields[2:], " ")); err == nil {
			files[filepath.Clean(name)] = true
		}
	}
	return files
}

func nativeFunctionDefinition(node clangASTNode) bool {
	for _, child := range node.Inner {
		if child.Kind == "CompoundStmt" {
			return true
		}
	}
	return false
}

func normalizeNativeType(typ string) string {
	typ = strings.Join(strings.Fields(strings.TrimSpace(typ)), " ")
	typ = strings.ReplaceAll(typ, " *", "*")
	typ = strings.ReplaceAll(typ, " &", "&")
	return typ
}

func nativeResultType(functionType string) string {
	functionType = normalizeNativeType(functionType)
	if i := strings.IndexByte(functionType, '('); i >= 0 {
		return normalizeNativeType(functionType[:i])
	}
	return functionType
}

func nativeBridgeType(language, typ string, result bool) (string, bool) {
	typ = normalizeNativeType(typ)
	switch typ {
	case "void":
		return "nil", result
	case "bool", "_Bool":
		return "bool", true
	case "char", "signed char", "unsigned char", "short", "unsigned short", "int", "unsigned int", "long", "unsigned long", "long long", "unsigned long long",
		"int8_t", "uint8_t", "int16_t", "uint16_t", "int32_t", "uint32_t", "int64_t", "uint64_t", "size_t", "ssize_t":
		return "int", true
	case "float", "double":
		return "float64", true
	case "const char*", "char const*":
		return "string", true
	case "char*":
		return "string", !result
	case "std::string", "const std::string&", "std::string const&":
		return "string", language == "cpp"
	}
	return "", false
}

func nativeWorkerSource(language, source string, exports []nativeExport) (string, error) {
	if language == "cpp" {
		return cppWorkerSource(source, exports), nil
	}
	return cWorkerSource(source, exports), nil
}

func cppWorkerSource(source string, exports []nativeExport) string {
	var out strings.Builder
	out.WriteString(source)
	out.WriteString(`
#line 1 "<bash++ C++ worker>"
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <iostream>
#include <stdexcept>
#include <string>
static std::string __bpp_hex(const std::string& s) { static const char h[]="0123456789abcdef"; std::string o; for(unsigned char c:s){o+=h[c>>4];o+=h[c&15];} return o; }
static std::string __bpp_unhex(const std::string& s) { if(s.size()%2) throw std::runtime_error("invalid string argument"); std::string o; for(size_t i=0;i<s.size();i+=2) o.push_back((char)std::stoi(s.substr(i,2),nullptr,16)); return o; }
// User code owns cout's flags, exception mask and formatting. Flush its buffer
// directly (also when sync_with_stdio(false)), without clearing that state.
// A user streambuf failure must not prevent the independent result transport.
static void __bpp_frame(const char* status,const char* kind,const std::string& value){
  const std::string frame=std::string("\x1e" "BASHPP\t")+status+"\t"+kind+"\t"+__bpp_hex(value)+"\n";
  try { if(auto* buffer=std::cout.rdbuf()) buffer->pubsync(); } catch(...) {}
  if(std::fwrite(frame.data(),1,frame.size(),stdout)!=frame.size() || std::fflush(stdout)!=0) std::exit(74);
}
static void __bpp_ok(const char* kind,const std::string& value){__bpp_frame("OK",kind,value);}
static void __bpp_err(const std::string& value){__bpp_frame("ERR","error",value);}
int main(int argc,char** argv){ try { if(argc<2) throw std::runtime_error("missing C++ function name"); std::string fn=argv[1];
`)
	for _, export := range exports {
		fmt.Fprintf(&out, "if(fn==%q){ if(argc!=%d) throw std::runtime_error(\"C++ function %s expects %d arguments\");\n", export.Name, len(export.params)+2, export.Name, len(export.params))
		args := make([]string, len(export.params))
		for i, typ := range export.params {
			args[i] = cppArgumentExpr(typ, fmt.Sprintf("argv[%d]", i+2))
		}
		call := export.Name + "(" + strings.Join(args, ",") + ")"
		if export.result == "void" {
			fmt.Fprintf(&out, "%s; __bpp_ok(\"nil\",\"\"); return 0;}\n", call)
		} else {
			fmt.Fprintf(&out, "auto value=%s; %s return 0;}\n", call, cppResultStmt(export.result))
		}
	}
	out.WriteString(`throw std::runtime_error("unknown C++ function "+fn); } catch(const std::exception& e){__bpp_err(e.what());} catch(...){__bpp_err("C++ function threw an unknown exception");} return 0; }
`)
	return out.String()
}

func cppArgumentExpr(typ, arg string) string {
	switch nativeMustBridge("cpp", typ) {
	case "string":
		if typ == "const char*" || typ == "char const*" || typ == "char*" {
			return "__bpp_unhex(" + arg + ").c_str()"
		}
		return "__bpp_unhex(" + arg + ")"
	case "bool":
		return "(std::string(" + arg + ")==\"true\")"
	case "float64":
		return "static_cast<" + typ + ">(std::stod(" + arg + "))"
	default:
		if strings.Contains(typ, "unsigned") || strings.HasPrefix(typ, "uint") || typ == "size_t" {
			return "static_cast<" + typ + ">(std::stoull(" + arg + "))"
		}
		return "static_cast<" + typ + ">(std::stoll(" + arg + "))"
	}
}

func cppResultStmt(typ string) string {
	switch nativeMustBridge("cpp", typ) {
	case "string":
		if typ == "const char*" || typ == "char const*" {
			return "if(!value) throw std::runtime_error(\"C++ function returned null string\"); __bpp_ok(\"string\",value);"
		}
		return "__bpp_ok(\"string\",value);"
	case "bool":
		return "__bpp_ok(\"bool\",value?\"true\":\"false\");"
	case "float64":
		return "__bpp_ok(\"float64\",std::to_string(value));"
	default:
		return "__bpp_ok(\"int\",std::to_string(value));"
	}
}

func nativeMustBridge(language, typ string) string {
	bridge, _ := nativeBridgeType(language, typ, false)
	return bridge
}

func cWorkerSource(source string, exports []nativeExport) string {
	var out strings.Builder
	out.WriteString(source)
	out.WriteString(`
#line 1 "<bash++ C worker>"
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
static char* __bpp_hex(const unsigned char* s,size_t n){static const char h[]="0123456789abcdef";char* o=(char*)malloc(n*2+1);if(!o)return NULL;for(size_t i=0;i<n;i++){o[i*2]=h[s[i]>>4];o[i*2+1]=h[s[i]&15];}o[n*2]=0;return o;}
static int __bpp_nib(char c){if(c>='0'&&c<='9')return c-'0';if(c>='a'&&c<='f')return c-'a'+10;if(c>='A'&&c<='F')return c-'A'+10;return -1;}
static char* __bpp_unhex(const char* s){size_t n=strlen(s);if(n%2)return NULL;char* o=(char*)malloc(n/2+1);if(!o)return NULL;for(size_t i=0;i<n;i+=2){int a=__bpp_nib(s[i]),b=__bpp_nib(s[i+1]);if(a<0||b<0){free(o);return NULL;}o[i/2]=(char)((a<<4)|b);}o[n/2]=0;return o;}
static void __bpp_frame(const char* status,const char* kind,const char* value){char* h=__bpp_hex((const unsigned char*)value,strlen(value));printf("\x1e" "BASHPP\t%s\t%s\t%s\n",status,kind,h?h:"");free(h);}
static long long __bpp_i(const char* s){char* e;errno=0;long long v=strtoll(s,&e,10);if(errno||*e){__bpp_frame("ERR","error","invalid integer argument");exit(0);}return v;}
static unsigned long long __bpp_u(const char* s){char* e;errno=0;if(*s=='-'){__bpp_frame("ERR","error","negative unsigned argument");exit(0);}unsigned long long v=strtoull(s,&e,10);if(errno||*e){__bpp_frame("ERR","error","invalid unsigned argument");exit(0);}return v;}
static double __bpp_f(const char* s){char* e;errno=0;double v=strtod(s,&e);if(errno||*e){__bpp_frame("ERR","error","invalid floating argument");exit(0);}return v;}
int main(int argc,char** argv){if(argc<2){__bpp_frame("ERR","error","missing C function name");return 0;}
`)
	for _, export := range exports {
		fmt.Fprintf(&out, "if(strcmp(argv[1],%q)==0){if(argc!=%d){__bpp_frame(\"ERR\",\"error\",\"C function %s received the wrong argument count\");return 0;}\n", export.Name, len(export.params)+2, export.Name)
		args := make([]string, len(export.params))
		for i, typ := range export.params {
			arg := fmt.Sprintf("argv[%d]", i+2)
			if nativeMustBridge("c", typ) == "string" {
				local := fmt.Sprintf("__bpp_s%d", i)
				fmt.Fprintf(&out, "char* %s=__bpp_unhex(%s);if(!%s){__bpp_frame(\"ERR\",\"error\",\"invalid string argument\");return 0;}\n", local, arg, local)
				args[i] = local
			} else {
				args[i] = cArgumentExpr(typ, arg)
			}
		}
		call := export.Name + "(" + strings.Join(args, ",") + ")"
		if export.result == "void" {
			fmt.Fprintf(&out, "%s;__bpp_frame(\"OK\",\"nil\",\"\");return 0;}\n", call)
		} else {
			fmt.Fprintf(&out, "%s value=%s;%s return 0;}\n", export.result, call, cResultStmt(export.result))
		}
	}
	out.WriteString(`__bpp_frame("ERR","error","unknown C function");return 0;}
`)
	return out.String()
}

func cArgumentExpr(typ, arg string) string {
	switch nativeMustBridge("c", typ) {
	case "bool":
		return "(" + arg + "[0]=='t')"
	case "float64":
		return "(" + typ + ")__bpp_f(" + arg + ")"
	default:
		if strings.Contains(typ, "unsigned") || strings.HasPrefix(typ, "uint") || typ == "size_t" {
			return "(" + typ + ")__bpp_u(" + arg + ")"
		}
		return "(" + typ + ")__bpp_i(" + arg + ")"
	}
}

func cResultStmt(typ string) string {
	switch nativeMustBridge("c", typ) {
	case "string":
		return `if(!value){__bpp_frame("ERR","error","C function returned null string");return 0;}__bpp_frame("OK","string",value);`
	case "bool":
		return `__bpp_frame("OK","bool",value?"true":"false");`
	case "float64":
		return `char b[64];snprintf(b,sizeof b,"%.17g",(double)value);__bpp_frame("OK","float64",b);`
	default:
		if strings.Contains(typ, "unsigned") || strings.HasPrefix(typ, "uint") || typ == "size_t" {
			return `char b[64];snprintf(b,sizeof b,"%llu",(unsigned long long)value);__bpp_frame("OK","int",b);`
		}
		return `char b[64];snprintf(b,sizeof b,"%lld",(long long)value);__bpp_frame("OK","int",b);`
	}
}

func (m *Module) callNativeArtifact(ctx context.Context, language, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if len(kwargs) != 0 {
		return CallResult{}, fmt.Errorf("%s functions do not accept named arguments", language)
	}
	if m.tempDir == "" {
		dir, err := os.MkdirTemp("", "bashpp-native-run-")
		if err != nil {
			return CallResult{}, err
		}
		path := filepath.Join(dir, "module")
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		if err := os.WriteFile(path, []byte(m.plan.Artifact), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return CallResult{}, err
		}
		m.tempDir = dir
	}
	path := filepath.Join(m.tempDir, "module")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	argv := []string{name}
	var params []string
	for _, export := range m.plan.Exports {
		if export.Name == name {
			params = export.Signature.Params
			break
		}
	}
	for i, arg := range args {
		encoded := fmt.Sprint(arg)
		if i < len(params) && params[i] == "string" {
			encoded = fmt.Sprintf("%x", []byte(encoded))
		}
		argv = append(argv, encoded)
	}
	cmd := exec.CommandContext(ctx, path, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	return decodeNativeResult(stdout.Bytes(), stderr.String(), runErr, language)
}

// decodeNativeResult runs after process completion, including exit callbacks.
func decodeNativeResult(data []byte, stderr string, runErr error, language string) (CallResult, error) {
	marker := []byte("\x1eBASHPP\t")
	result := CallResult{Stdout: string(data), Stderr: stderr}
	index := bytes.Index(data, marker)
	if index < 0 {
		if runErr != nil {
			return result, fmt.Errorf("%s worker failed: %w", language, runErr)
		}
		return result, fmt.Errorf("%s worker returned no result frame", language)
	}
	// The stdout protocol cannot distinguish a second frame from callback
	// data imitating one. Reject ambiguity instead of selecting a value.
	if bytes.Contains(data[index+len(marker):], marker) {
		return result, fmt.Errorf("multiple %s worker result frames", language)
	}
	payload := data[index+len(marker):]
	end := bytes.IndexByte(payload, '\n')
	if end < 0 {
		return result, fmt.Errorf("invalid %s worker result frame", language)
	}
	result.Stdout = string(data[:index]) + string(payload[end+1:])
	payload = bytes.TrimSuffix(payload[:end], []byte("\r"))
	// C atexit handlers and C++ static destructors run after the return
	// frame. Their output and native process failure remain observable.
	if runErr != nil {
		return result, fmt.Errorf("%s worker failed: %w", language, runErr)
	}
	fields := strings.Split(string(payload), "\t")
	if len(fields) != 3 {
		return result, fmt.Errorf("invalid %s worker result frame", language)
	}
	decoded, decodeErr := hex.DecodeString(fields[2])
	if decodeErr != nil {
		return result, fmt.Errorf("invalid %s worker result payload", language)
	}
	if fields[0] != "OK" && fields[0] != "ERR" {
		return result, fmt.Errorf("invalid %s worker result status", language)
	}
	if fields[0] == "ERR" {
		return result, errors.New(string(decoded))
	}
	switch fields[1] {
	case "nil":
		result.Value = nil
	case "string":
		result.Value = string(decoded)
	case "bool":
		result.Value, decodeErr = strconv.ParseBool(string(decoded))
	case "int":
		result.Value, decodeErr = strconv.ParseInt(string(decoded), 10, 64)
	case "float64":
		result.Value, decodeErr = strconv.ParseFloat(string(decoded), 64)
	default:
		decodeErr = fmt.Errorf("unknown %s result type %q", language, fields[1])
	}
	if decodeErr != nil {
		return result, decodeErr
	}
	return result, nil
}
