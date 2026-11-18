package polyglot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TypeScript uses the official microsoft/TypeScript compiler under Node.
// CompilerModule may be a Node module name or an absolute typescript.js path.
type TypeScript struct {
	NodeCommand    string
	BunCommand     string
	Runtime        string
	CompilerModule string
	Environment    *EnvironmentPlan
	// JavaScript parses the fence as JavaScript: syntax is checked, types are
	// not, and an export is typed only where JSDoc says so.
	JavaScript bool
}

func (t TypeScript) executable() string {
	if t.Environment != nil && t.Environment.Executable != "" {
		return t.Environment.Executable
	}
	if t.NodeCommand != "" {
		return t.NodeCommand
	}
	if t.BunCommand != "" {
		return t.BunCommand
	}
	runtime := t.runtimeName()
	if runtime == "bun" {
		if command := os.Getenv("BASHPP_BUN"); command != "" {
			return command
		}
		return "bun"
	}
	if command := os.Getenv("BASHPP_NODE"); command != "" {
		return command
	}
	return "node"
}

func (t TypeScript) runtimeName() string {
	if t.Environment != nil && t.Environment.Runtime != "" {
		return t.Environment.Runtime
	}
	if t.Runtime != "" {
		return strings.ToLower(t.Runtime)
	}
	if t.BunCommand != "" {
		return "bun"
	}
	if t.NodeCommand != "" {
		return "node"
	}
	if runtime := strings.ToLower(os.Getenv("BASHPP_TYPESCRIPT_RUNTIME")); runtime != "" {
		return runtime
	}
	return "node"
}

func (t TypeScript) compilerModule() string {
	if t.Environment != nil && t.Environment.CompilerModule != "" {
		return t.Environment.CompilerModule
	}
	if t.CompilerModule != "" {
		return t.CompilerModule
	}
	if module := os.Getenv("BASHPP_TYPESCRIPT_MODULE"); module != "" {
		return module
	}
	return "typescript"
}

func (t TypeScript) name() string {
	if t.JavaScript {
		return "JavaScript"
	}
	return "TypeScript"
}

func (t TypeScript) analyzeKind() string {
	if t.JavaScript {
		return "js"
	}
	return "ts"
}
func (t TypeScript) arguments(Plan) []string {
	return append(leadingArgs(t.Environment), "-e", typeScriptWorker)
}
func (t TypeScript) loadRequest(plan Plan) map[string]any {
	dir := ""
	if t.Environment != nil {
		dir = t.Environment.SourceDir
		if dir == "" {
			dir = t.Environment.Dir
		}
	}
	return map[string]any{"id": 0, "op": "load", "artifact": plan.Artifact, "dir": dir}
}

func (t TypeScript) configure(cmd *exec.Cmd) {
	if t.Environment == nil {
		return
	}
	cmd.Dir = t.Environment.Dir
	cmd.Env = append([]string(nil), t.Environment.Env...)
}

func (t TypeScript) Analyze(ctx context.Context, source string) ([]Export, error) {
	exports, _, err := t.AnalyzeArtifact(ctx, source)
	return exports, err
}

func (t TypeScript) AnalyzeArtifact(ctx context.Context, source string) ([]Export, string, error) {
	input := "<bash++ typescript>.ts"
	if t.JavaScript {
		input = "<bash++ javascript>.js"
	}
	if t.Environment != nil && t.Environment.Dir != "" {
		dir := t.Environment.SourceDir
		if dir == "" {
			dir = t.Environment.Dir
		}
		input = filepath.Join(dir, ".bashpp-fence."+t.analyzeKind())
	}
	cmd := exec.CommandContext(ctx, t.executable(), append(leadingArgs(t.Environment), "-e", typeScriptAnalyze, t.compilerModule(), input, t.analyzeKind())...)
	t.configure(cmd)
	cmd.Stdin = strings.NewReader(source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("TypeScript runtime unavailable: %w", err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, "", errors.New(msg)
	}
	var response struct {
		OK       bool     `json:"ok"`
		V7       bool     `json:"v7"`
		Root     string   `json:"root"`
		Exports  []Export `json:"exports"`
		Artifact string   `json:"artifact"`
		Error    string   `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, "", fmt.Errorf("invalid TypeScript analyzer response: %w", err)
	}
	if response.V7 {
		args := []string{"--input-type=module", "-e", typeScriptAnalyze7, response.Root, filepath.Dir(input), t.analyzeKind()}
		if t.runtimeName() == "bun" {
			args = args[1:]
		}
		cmd = exec.CommandContext(ctx, t.executable(), append(leadingArgs(t.Environment), args...)...)
		t.configure(cmd)
		cmd.Stdin = strings.NewReader(source)
		stdout.Reset()
		stderr.Reset()
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, "", errors.New(msg)
		}
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			return nil, "", fmt.Errorf("invalid TypeScript 7 analyzer response: %w", err)
		}
	}
	if !response.OK {
		return nil, "", errors.New(response.Error)
	}
	return response.Exports, response.Artifact, nil
}

// typeScriptNodeAmbient builds loose ambient declarations for the Node
// built-in modules and the globals a fence reaches for first. It is used only
// when no @types/node is visible, so a fence importing node:fs or path checks
// without a project; a project that has the real types keeps them.
const typeScriptNodeAmbient = `
function nodeAmbient(builtins) {
  const names = new Set(['node:*']);
  for (const name of builtins) if (!name.startsWith('_') && !name.startsWith('internal/')) names.add(name);
  return [...names].map(n => 'declare module ' + JSON.stringify(n) + ';').join('\n') +
    '\ndeclare var process: any;\ndeclare var Buffer: any;\n';
}
`

const typeScriptAnalyze = typeScriptNodeAmbient + `
const fs = require('fs');
const cp = require('child_process');
const path = require('path');
const requested = process.argv[1] || 'typescript';
const input = process.argv[2] || '<bash++ typescript>.ts';
const js = process.argv[3] === 'js';
const label = js ? '<bash++ javascript>' : '<bash++ typescript>';
function loadCompiler(name) {
  try {
    let entry, packageFile;
	if (path.isAbsolute(name)) {
	  if (fs.statSync(name).isDirectory()) {
	    packageFile = path.join(name, 'package.json');
	    const manifest = require(packageFile);
	    entry = path.join(name, manifest.main || 'lib/version.cjs');
	  } else {
	    entry = name;
	    let dir = path.dirname(name);
	    while (dir !== path.dirname(dir) && !fs.existsSync(path.join(dir, 'package.json'))) dir = path.dirname(dir);
	    packageFile = path.join(dir, 'package.json');
	  }
	} else {
      entry = require.resolve(name);
      packageFile = require.resolve(name + '/package.json');
    }
    return {compiler:require(entry),root:path.dirname(packageFile)};
  }
  catch (first) {
    if (name !== 'typescript') throw new Error('official TypeScript compiler module unavailable at ' + name + ': ' + first.message);
    try {
      const root = cp.execFileSync('npm', ['root', '-g'], {encoding:'utf8'}).trim();
      const packageRoot = path.join(root, 'typescript');
      return {compiler:require(packageRoot),root:packageRoot};
    } catch (_) {
      throw new Error('official TypeScript compiler module unavailable; install it with npm install -g typescript or set BASHPP_TYPESCRIPT_MODULE');
    }
  }
}
function fail(message) { process.stdout.write(JSON.stringify({ok:false,error:message})); process.exit(0); }
function diagnostic(ts, d) {
  const message = ts.flattenDiagnosticMessageText(d.messageText, '\n');
  if (!d.file || d.start == null) return message;
  const p = d.file.getLineAndCharacterOfPosition(d.start);
  return label + ':' + (p.line + 1) + ':' + (p.character + 1) + ': ' + message;
}
try {
  const loaded = loadCompiler(requested), ts = loaded.compiler;
  if (typeof ts.createProgram !== 'function') {
    process.stdout.write(JSON.stringify({v7:true,root:loaded.root})); process.exit(0);
  }
  const original = fs.readFileSync(0, 'utf8');
  const scriptKind = js ? ts.ScriptKind.JS : ts.ScriptKind.TS;
  const parsed = ts.createSourceFile(input, original, ts.ScriptTarget.Latest, true, scriptKind);
  const names = [], namesToExport = [];
  for (const node of parsed.statements) {
    if (ts.isFunctionDeclaration(node) && node.name && node.body) {
      if (!node.name.text.startsWith('_')) {
        names.push(node.name.text);
        const exported = node.modifiers && node.modifiers.some(m => m.kind === ts.SyntaxKind.ExportKeyword);
        if (!exported) namesToExport.push(node.name.text);
      }
      continue;
    }
    // Imports and ordinary module initialization are part of the module. Only
    // function declarations become Bash++ callables.
  }
  const source = original + (namesToExport.length ? '\nexport { ' + namesToExport.join(', ') + ' };\n' : '');
	  const modern = ts.ModuleKind.ESNext !== undefined && ts.ModuleResolutionKind.Bundler !== undefined;
	  const options = {target:ts.ScriptTarget.ES2022,
	    module:modern ? ts.ModuleKind.ESNext : ts.ModuleKind.CommonJS,
	    moduleResolution:modern ? ts.ModuleResolutionKind.Bundler : ts.ModuleResolutionKind.Node10, skipLibCheck:true,
    noEmitOnError:true, strict:false, sourceMap:false, declaration:false};
  if (Number.parseInt(ts.versionMajorMinor || ts.version || '0', 10) >= 6) options.ignoreDeprecations = '6.0';
  // The ESM artifact comes from transpileModule below, so the checking program
  // never emits; that lets it accept an explicit .ts specifier — the spelling
  // Node's native type stripping requires for a relative source import, and
  // one project tsconfigs enable with allowImportingTsExtensions.
  if (modern) { options.noEmit = true; options.allowImportingTsExtensions = true; }
  if (js) { options.noEmit = true; options.allowJs = true; options.checkJs = false; }
  const host = ts.createCompilerHost(options);
  const ambientName = path.join(path.dirname(input), '.bashpp-node-ambient.d.ts');
  const hasNodeTypes = typeof ts.getAutomaticTypeDirectiveNames === 'function' &&
    ts.getAutomaticTypeDirectiveNames(options, host).includes('node');
  const ambient = hasNodeTypes ? '' : nodeAmbient(require('module').builtinModules);
  // The compiler hands the host NORMALIZED names (forward slashes; on
  // Windows the drive letter's case is not guaranteed), while input is the
  // OS spelling it was given: compare through the same normalization.
  const norm = f => { f = String(f).replace(/\\/g, '/'); return process.platform === 'win32' ? f.toLowerCase() : f; };
  const inputKey = norm(input);
  const isInput = file => norm(file) === inputKey;
  const isAmbient = file => ambient !== '' && norm(file) === norm(ambientName);
  const baseGet = host.getSourceFile.bind(host);
  host.getSourceFile = (file, version, onError, fresh) =>
    isInput(file) ? ts.createSourceFile(input, source, version, true, scriptKind) :
    isAmbient(file) ? ts.createSourceFile(ambientName, ambient, version, true, ts.ScriptKind.TS) : baseGet(file, version, onError, fresh);
  host.fileExists = ((base) => file => isInput(file) || isAmbient(file) || base(file))(host.fileExists.bind(host));
  host.readFile = ((base) => file => isInput(file) ? source : isAmbient(file) ? ambient : base(file))(host.readFile.bind(host));
  let artifact = '';
  host.writeFile = (file, text) => {
	    const output = path.basename(input).replace(/\.[cm]?tsx?$/, '.js');
	    if (path.basename(file) === output) artifact = text;
  };
  const program = ts.createProgram(ambient ? [input, ambientName] : [input], options, host);
  const sourceFile = program.getSourceFile(input);
  const checker = program.getTypeChecker();
	  const diagnostics = ts.getPreEmitDiagnostics(program).filter(d => !d.file || isInput(d.file.fileName));
  if (diagnostics.length) fail(diagnostics.map(d => diagnostic(ts, d)).join('\n'));
  const exports = [];
  // A JavaScript export is typed only where JSDoc says so; a type inferred
  // from the body or a default value is a guess the caller never wrote.
  const documentedParam = d => !js || (!!d && (ts.getJSDocParameterTags(d).some(t => t.typeExpression) || !!ts.getJSDocType(d)));
  const documentedResult = n => !js || !!ts.getJSDocReturnType(n);
  const mapType = type => {
    if (type.flags & ts.TypeFlags.StringLike) return 'string';
    if (type.flags & ts.TypeFlags.NumberLike) return 'float64';
    if (type.flags & ts.TypeFlags.BooleanLike) return 'bool';
    if (type.flags & (ts.TypeFlags.Void | ts.TypeFlags.Undefined | ts.TypeFlags.Never)) return 'nil';
    const shown = checker.typeToString(type);
    if (shown === 'Uint8Array' || shown.startsWith('Uint8Array<')) return 'bytes';
    if (shown === 'any' || shown === 'unknown') return 'any';
    return '';
  };
  for (const node of sourceFile.statements) {
    if (!ts.isFunctionDeclaration(node) || !node.name || !node.body || node.name.text.startsWith('_')) continue;
    const sig = checker.getSignatureFromDeclaration(node);
    let dynamic = !sig;
    const params = [];
    if (sig) for (const symbol of sig.parameters) {
      const mapped = documentedParam(symbol.valueDeclaration) ? mapType(checker.getTypeOfSymbolAtLocation(symbol, node)) : '';
      params.push(mapped || 'any'); dynamic ||= !mapped || mapped === 'any';
    }
    let results = [];
    if (sig) {
      const returned = checker.getReturnTypeOfSignature(sig);
      const awaited = checker.getPromisedTypeOfPromise ? checker.getPromisedTypeOfPromise(returned) : undefined;
      const mapped = documentedResult(node) ? mapType(awaited || returned) : 'any';
      if (!mapped) dynamic = true;
      else if (mapped !== 'nil') results = [mapped];
    }
    exports.push({name:node.name.text,signature:{params,results,dynamic}});
  }
	  if (modern || js) artifact = ts.transpileModule(source, {compilerOptions:{
	    target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ESNext,sourceMap:false,declaration:false
	  },fileName:input}).outputText;
	  else {
	    const emitted = program.emit();
	    if (emitted.emitSkipped || !artifact) fail('TypeScript compiler did not emit the module');
	  }
  process.stdout.write(JSON.stringify({ok:true,exports,artifact}));
} catch (error) {
  process.stderr.write(String(error && error.stack || error));
  process.exit(1);
}
`

const typeScriptAnalyze7 = typeScriptNodeAmbient + `
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import cp from 'node:child_process';
import {builtinModules} from 'node:module';
import {pathToFileURL} from 'node:url';
const root = process.argv[1];
const projectDir = process.argv[2] || process.cwd();
const apiModule = await import(pathToFileURL(path.join(root, 'dist/api/sync/api.js')));
const ast = await import(pathToFileURL(path.join(root, 'dist/ast/is.js')));
const original = fs.readFileSync(0, 'utf8');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'bashpp-typescript-'));
const js = process.argv[3] === 'js';
const ext = js ? 'js' : 'ts';
const label = js ? '<bash++ javascript>' : '<bash++ typescript>';
const input = path.join(dir, 'module.' + ext);
let api;
function fail(message) { process.stdout.write(JSON.stringify({ok:false,error:message})); process.exit(0); }
function location(sourceFile, node) {
  const p = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
  return label + ':' + (p.line + 1) + ':' + (p.character + 1);
}
try {
	let packageDir = projectDir, modulesDir = projectDir;
	while (packageDir !== path.dirname(packageDir) && !fs.existsSync(path.join(packageDir, 'package.json'))) packageDir = path.dirname(packageDir);
	while (modulesDir !== path.dirname(modulesDir) && !fs.existsSync(path.join(modulesDir, 'node_modules'))) modulesDir = path.dirname(modulesDir);
	if (fs.existsSync(path.join(packageDir, 'package.json'))) {
	  fs.copyFileSync(path.join(packageDir, 'package.json'), path.join(dir, 'package.json'));
	  const sourceDir = path.join(packageDir, 'src');
	  if (fs.existsSync(sourceDir)) fs.symlinkSync(sourceDir, path.join(dir, 'src'), 'junction');
	}
	if (fs.existsSync(path.join(modulesDir, 'node_modules'))) fs.symlinkSync(path.join(modulesDir, 'node_modules'), path.join(dir, 'node_modules'), 'junction');
	  const nodeTypes = fs.existsSync(path.join(modulesDir, 'node_modules', '@types', 'node'));
	  if (!nodeTypes) fs.writeFileSync(path.join(dir, 'node-ambient.d.ts'), nodeAmbient(builtinModules));
	  fs.writeFileSync(path.join(dir, 'tsconfig.json'), JSON.stringify({compilerOptions:{
	    target:'es2022',module:'esnext',moduleResolution:'bundler',outDir:'dist',strict:false,skipLibCheck:true,
    noEmitOnError:true,sourceMap:false,declaration:false,allowJs:js,checkJs:false,types:nodeTypes ? ['node'] : []},include:nodeTypes ? ['module.' + ext] : ['module.' + ext,'node-ambient.d.ts']}));
  fs.writeFileSync(input, original);
  api = new apiModule.API({cwd:dir});
  let snapshot = api.updateSnapshot({openFiles:[input]});
  let project = snapshot.getDefaultProjectForFile(input);
  if (!project) fail('TypeScript 7 did not create a project for the fenced source');
  let sourceFile = project.program.getSourceFile(input);
  const names = [], namesToExport = [];
  for (const node of sourceFile.statements) {
    if (ast.isFunctionDeclaration(node) && node.name && node.body) {
      if (!node.name.text.startsWith('_')) {
        names.push(node.name.text);
        const exported = node.modifiers && node.modifiers.some(m => sourceFile.text.slice(m.pos,m.end).replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g,'').trim() === 'export');
        if (!exported) namesToExport.push(node.name.text);
      }
      continue;
    }
    // Imports and ordinary module initialization remain in the emitted module.
  }
  if (namesToExport.length) {
    fs.writeFileSync(input, original + '\nexport { ' + namesToExport.join(', ') + ' };\n');
    snapshot.dispose();
    snapshot = api.updateSnapshot({openFiles:[input]});
    project = snapshot.getDefaultProjectForFile(input);
    sourceFile = project.program.getSourceFile(input);
  }
  const diagnostics = [...project.program.getSyntacticDiagnostics(input), ...project.program.getSemanticDiagnostics(input), ...project.program.getProgramDiagnostics()];
  if (diagnostics.length) {
    fail(diagnostics.map(d => {
      if (!d.fileName || d.pos == null) return d.text;
      const file = project.program.getSourceFile(d.fileName);
      const p = file && file.getLineAndCharacterOfPosition(d.pos);
      return p ? label + ':' + (p.line+1) + ':' + (p.character+1) + ': ' + d.text : d.text;
    }).join('\n'));
  }
  const checker = project.checker, exports = [];
  const mapType = type => {
    const shown = checker.typeToString(type);
    if (shown === 'string') return 'string';
    if (shown === 'number') return 'float64';
    if (shown === 'boolean' || shown === 'true' || shown === 'false') return 'bool';
    if (shown === 'void' || shown === 'undefined' || shown === 'never') return 'nil';
    if (shown === 'Uint8Array' || shown.startsWith('Uint8Array<')) return 'bytes';
    if (shown === 'any' || shown === 'unknown') return 'any';
    return '';
  };
  for (const node of sourceFile.statements) {
    if (!ast.isFunctionDeclaration(node) || !node.name || !node.body || node.name.text.startsWith('_')) continue;
    const type = checker.getTypeAtLocation(node.name);
    const sig = checker.getSignaturesOfType(type, apiModule.SignatureKind.Call)[0];
    let dynamic = !sig, params = [], results = [];
    if (js) {
      // The TypeScript 7 API exposes no JSDoc reader: every JavaScript export is dynamic.
      exports.push({name:node.name.text,signature:{params:sig ? sig.getParameters().map(() => 'any') : [],results:['any'],dynamic:true}});
      continue;
    }
    if (sig) for (const symbol of sig.getParameters()) {
      const mapped = mapType(checker.getTypeOfSymbolAtLocation(symbol,node));
      params.push(mapped || 'any'); dynamic ||= !mapped || mapped === 'any';
    }
    if (sig) {
      const returned = checker.getReturnTypeOfSignature(sig);
      const awaited = checker.getPromisedTypeOfPromise ? checker.getPromisedTypeOfPromise(returned) : undefined;
      const mapped = mapType(awaited || returned);
      if (!mapped) dynamic = true; else if (mapped !== 'nil') results = [mapped];
    }
    exports.push({name:node.name.text,signature:{params,results,dynamic}});
  }
  snapshot.dispose(); api.close(); api = undefined;
  const tsc = path.join(root, 'bin/tsc');
  const emitted = cp.spawnSync(process.execPath,[tsc,'-p',path.join(dir,'tsconfig.json'),'--pretty','false'],{encoding:'utf8'});
  if (emitted.status !== 0) fail((emitted.stdout || emitted.stderr || 'TypeScript 7 emit failed').trim());
	  const artifact = fs.readFileSync(path.join(dir,'dist/module.js'),'utf8');
  process.stdout.write(JSON.stringify({ok:true,exports,artifact}));
} finally {
  if (api) api.close();
  fs.rmSync(dir,{recursive:true,force:true});
}
`

const typeScriptWorker = `
const readline = require('readline');
const fs = require('fs');
const path = require('path');
const {pathToFileURL} = require('url');
const util = require('util');
let protocolWrite;
try {
  fs.fstatSync(3);
  const protocol = fs.createWriteStream(null, {fd:3, autoClose:false});
  protocolWrite = text => protocol.write(text);
} catch (_) {
  protocolWrite = process.stdout.write.bind(process.stdout);
}
let moduleExports = Object.create(null), currentOut = '', currentErr = '';
let moduleDir = '';
const consoleBridge = {
  log: (...args) => { currentOut += util.format(...args) + '\n'; },
  info: (...args) => { currentOut += util.format(...args) + '\n'; },
  warn: (...args) => { currentErr += util.format(...args) + '\n'; },
  error: (...args) => { currentErr += util.format(...args) + '\n'; }
};
function dec(v) {
  if (v && typeof v === 'object' && Object.keys(v).length === 1 && '$bytes' in v) return Uint8Array.from(Buffer.from(v.$bytes, 'base64'));
  if (v && typeof v === 'object' && Object.keys(v).length === 1 && '$bigint' in v) return BigInt(v.$bigint);
  if (Array.isArray(v)) return v.map(dec);
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k,x]) => [k,dec(x)]));
  return v;
}
function enc(v) {
  if (v === undefined || v === null) return null;
  if (typeof v === 'bigint') return {$bigint:v.toString()};
  if (v instanceof Uint8Array) return {$bytes:Buffer.from(v).toString('base64')};
  if (Array.isArray(v)) return v.map(enc);
  if (v && Object.prototype.toString.call(v) === '[object Object]' &&
      (Object.getPrototypeOf(v) === Object.prototype || Object.getPrototypeOf(v) === null)) {
    return Object.fromEntries(Object.entries(v).map(([k,x]) => [k,enc(x)]));
  }
  if (typeof v === 'number' && !Number.isFinite(v)) throw new TypeError('unsupported non-finite number result');
  if (typeof v === 'boolean' || typeof v === 'number' || typeof v === 'string') return v;
  throw new TypeError('unsupported foreign result type: ' + typeof v);
}
const rl = readline.createInterface({input:process.stdin, crlfDelay:Infinity});
function findUp(start, name) {
  let dir = start;
  for (;;) {
    if (fs.existsSync(path.join(dir, name))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) return '';
    dir = parent;
  }
}
function linkPackageContext(sourceDir, targetDir) {
  const packageDir = findUp(sourceDir, 'package.json');
  if (packageDir) {
    fs.copyFileSync(path.join(packageDir, 'package.json'), path.join(targetDir, 'package.json'));
    for (const entry of fs.readdirSync(packageDir, {withFileTypes:true})) {
      if (entry.name === 'package.json' || entry.name === 'node_modules') continue;
      const target = path.join(targetDir, entry.name);
      if (fs.existsSync(target)) continue;
      const source = path.join(packageDir, entry.name);
      if (entry.isDirectory()) fs.symlinkSync(source, target, 'junction');
      else if (entry.isFile()) fs.copyFileSync(source, target);
    }
  }
  const modulesDir = findUp(sourceDir, 'node_modules');
  if (modulesDir) fs.symlinkSync(path.join(modulesDir, 'node_modules'), path.join(targetDir, 'node_modules'), 'junction');
}
rl.on('line', async line => {
  let req, response;
  try {
    req = JSON.parse(line); const id = req.id || 0;
    if (req.op === 'load') {
      const dir = req.dir || process.cwd();
      if (moduleDir) fs.rmSync(moduleDir, {recursive:true, force:true});
      moduleDir = req.module_dir;
      if (!moduleDir) throw new Error('missing TypeScript module directory');
      linkPackageContext(dir, moduleDir);
      const filename = path.join(moduleDir, 'module.mjs');
      fs.writeFileSync(filename, req.artifact);
      const priorConsole = globalThis.console;
      globalThis.console = consoleBridge;
      try {
        moduleExports = await import(pathToFileURL(filename).href + '?v=' + Date.now());
        response = {id,ok:true,stdout:currentOut,stderr:currentErr};
      } finally { globalThis.console = priorConsole; }
    } else if (req.op === 'call') {
      currentOut = ''; currentErr = '';
      const fn = moduleExports[req.name];
      if (typeof fn !== 'function') throw new TypeError('unknown TypeScript export ' + req.name);
      const priorConsole = globalThis.console;
      globalThis.console = consoleBridge;
      let value;
      try { value = await fn(...dec(req.args || [])); }
      finally { globalThis.console = priorConsole; }
      response = {id,ok:true,result:enc(value),stdout:currentOut,stderr:currentErr};
    } else throw new Error('unknown operation');
  } catch (error) {
    response = {id:req && req.id || 0,ok:false,error:String(error && error.stack || error),stdout:currentOut,stderr:currentErr};
  }
	  protocolWrite('\x1eBASHPP' + JSON.stringify(response) + '\n');
});
process.on('exit', () => { if (moduleDir) fs.rmSync(moduleDir, {recursive:true, force:true}); });
`
