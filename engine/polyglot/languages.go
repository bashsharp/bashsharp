package polyglot

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// LanguageRuntime is what a fence language row constructs for one source
// unit: the analyzer that turns a body into exports and the runtime the
// module runs on. Every shipped runtime satisfies both halves; an embedder
// without a worker process uses [Embedded].
type LanguageRuntime interface {
	Runtime
	Analyzer
}

// RuntimeConfig is what a source unit knows when it constructs a runtime.
// Environment is nil for a language whose row does not read one. Dir is the
// caller's directory when the unit was prepared; Cwd, when set, answers the
// caller's directory at call time (an interpreter whose script may `cd`
// before a fence call).
type RuntimeConfig struct {
	// Source is the original artifact path (the embedded file for embed).
	// Text adapters use it for source-relative imports and policy roots.
	Source      string
	Environment *EnvironmentPlan
	Dir         string
	Cwd         func() string
	Environ     []string
	// Env, when set, answers the caller's environment at call time, the way
	// Cwd answers its directory: a variable exported after the unit was
	// prepared reaches a verb called later.
	Env func() []string
}

// CallerDir answers the caller's directory now: Cwd when set, else Dir.
func (c RuntimeConfig) CallerDir() string {
	if c.Cwd != nil {
		if dir := c.Cwd(); dir != "" {
			return dir
		}
	}
	return c.Dir
}

// CallerEnv answers the caller's environment now: Env when set, else
// Environ.
func (c RuntimeConfig) CallerEnv() []string {
	if c.Env != nil {
		if env := c.Env(); env != nil {
			return env
		}
	}
	return c.Environ
}

// Language is one row of the fence language table: the spelling(s) a fence
// opener may use, how the interpreter constructs the runtime, how a lowered
// program constructs the same runtime, and the few per-language switches the
// engine used to hardcode. Rows are registered once at init by the package
// that owns the runtime — this package for the worker languages, the
// interpreter for its embedded dialect islands, an embedder for its own —
// and a later registration of the same canonical name replaces the row.
type Language struct {
	// Canonical is the name analyzers, plans and runtimes are keyed by.
	Canonical string
	// Aliases are the other opener spellings (`py` for `python`). An alias
	// is never a separate language.
	Aliases []string
	// NeedsEnvironment says the row reads source-relative environment
	// metadata ([DiscoverEnvironment]) before constructing its runtime.
	NeedsEnvironment bool
	// LineDirectives says same-language blocks aggregate with a `#line`
	// directive per block so diagnostics name the fence, not the module.
	LineDirectives bool
	// Callbacks says the runtime may call back into shell functions and the
	// module must be given the unit's callback table.
	Callbacks bool
	// Text says the row carries a text artifact, not source: its methods
	// are known only at prepare, so the fence needs an alias.
	Text bool
	// InterpretedOnly says the row's processor is the running shell itself
	// (a task file, a skill), so a lowered program cannot carry it and
	// lowering refuses the fence by name.
	InterpretedOnly bool
	// ModuleFor names the source language whose code fence, in the same
	// unit, builds against this row's manifest (`gomod` → `go`): the
	// materialized manifest becomes that fence's module file.
	ModuleFor string
	// NewRuntime constructs the runtime for one source unit.
	NewRuntime func(RuntimeConfig) LanguageRuntime
	// LoweredRuntime returns the Go expression a lowered program uses to
	// construct the same runtime; prefix is the emitter's import prefix and
	// environment the already-emitted `*EnvironmentPlan` literal or "nil".
	LoweredRuntime func(prefix, environment string) string
	// LoweredImports are the import paths that expression needs besides
	// this package; a lowered program imports each under prefix + its base
	// name.
	LoweredImports []string
}

var (
	languagesMu      sync.RWMutex
	languages        = map[string]Language{}
	languageAliases  = map[string]string{}
	languageRevision uint64
)

// RegisterLanguage adds or replaces a fence language row. It panics on a row
// without a canonical name or a constructor, the way a malformed table entry
// should fail at init and not at the first fence.
func RegisterLanguage(l Language) {
	l.Canonical = strings.ToLower(strings.TrimSpace(l.Canonical))
	if l.Canonical == "" {
		panic("polyglot: RegisterLanguage without a canonical name")
	}
	if l.NewRuntime == nil {
		panic(fmt.Sprintf("polyglot: RegisterLanguage(%s) without NewRuntime", l.Canonical))
	}
	languagesMu.Lock()
	defer languagesMu.Unlock()
	languageRevision++
	if old, ok := languages[l.Canonical]; ok {
		for _, alias := range old.Aliases {
			if languageAliases[alias] == old.Canonical {
				delete(languageAliases, alias)
			}
		}
	}
	languages[l.Canonical] = l
	for _, alias := range l.Aliases {
		languageAliases[strings.ToLower(strings.TrimSpace(alias))] = l.Canonical
	}
}

// LanguageRevision changes whenever a provider row is registered or replaced.
// A compiler can use it to reject reuse of a plan prepared before a row change.
func LanguageRevision() uint64 {
	languagesMu.RLock()
	defer languagesMu.RUnlock()
	return languageRevision
}

// LookupLanguage answers a canonical name or an alias.
func LookupLanguage(name string) (Language, bool) {
	languagesMu.RLock()
	defer languagesMu.RUnlock()
	l, ok := languages[canonicalLanguageLocked(name)]
	return l, ok
}

// Languages lists the registered rows sorted by canonical name.
func Languages() []Language {
	languagesMu.RLock()
	defer languagesMu.RUnlock()
	out := make([]Language, 0, len(languages))
	for _, l := range languages {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Canonical < out[j].Canonical })
	return out
}

// CanonicalLanguage maps a fence or import language spelling to the name the
// analyzers and runtimes are keyed by. Short spellings are aliases, never
// separate languages: `~~~ts` is `typescript` and `~~~py` is `python`. An
// unregistered spelling passes through unchanged so [Prepare] can name it
// in its refusal.
func CanonicalLanguage(language string) string {
	return canonicalLanguage(language)
}

func canonicalLanguage(language string) string {
	languagesMu.RLock()
	defer languagesMu.RUnlock()
	return canonicalLanguageLocked(language)
}

func canonicalLanguageLocked(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if canonical, ok := languageAliases[language]; ok {
		return canonical
	}
	return language
}

func languageFlag(language string, flag func(Language) bool) bool {
	l, ok := LookupLanguage(language)
	return ok && flag(l)
}

func loweredRuntime(typ string) func(prefix, environment string) string {
	return func(prefix, environment string) string {
		return fmt.Sprintf("%spolyglot.%s{Environment:%s}", prefix, typ, environment)
	}
}

func init() {
	RegisterLanguage(Language{Canonical: "python", Aliases: []string{"py"}, NeedsEnvironment: true, Callbacks: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return Python{Environment: cfg.Environment, Cwd: cfg.Cwd} },
		LoweredRuntime: loweredRuntime("Python")})
	RegisterLanguage(Language{Canonical: "typescript", Aliases: []string{"ts"}, NeedsEnvironment: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return TypeScript{Environment: cfg.Environment} },
		LoweredRuntime: loweredRuntime("TypeScript")})
	// JavaScript runs on the TypeScript worker: the same compiler parses the
	// fence (no type check), so one provisioned toolchain serves both rows.
	RegisterLanguage(Language{Canonical: "javascript", Aliases: []string{"js"}, NeedsEnvironment: true,
		NewRuntime: func(cfg RuntimeConfig) LanguageRuntime {
			return TypeScript{Environment: cfg.Environment, JavaScript: true}
		},
		LoweredRuntime: func(prefix, environment string) string {
			return fmt.Sprintf("%spolyglot.TypeScript{JavaScript:true,Environment:%s}", prefix, environment)
		}})
	RegisterLanguage(Language{Canonical: "rust", Aliases: []string{"rs"}, NeedsEnvironment: true, Callbacks: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return Rust{Environment: cfg.Environment} },
		LoweredRuntime: loweredRuntime("Rust")})
	RegisterLanguage(Language{Canonical: "c", NeedsEnvironment: true, LineDirectives: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return C{Environment: cfg.Environment} },
		LoweredRuntime: loweredRuntime("C")})
	RegisterLanguage(Language{Canonical: "cpp", Aliases: []string{"cxx"}, NeedsEnvironment: true, LineDirectives: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return CPP{Environment: cfg.Environment} },
		LoweredRuntime: loweredRuntime("CPP")})
	RegisterLanguage(Language{Canonical: "go", NeedsEnvironment: true,
		NewRuntime: func(cfg RuntimeConfig) LanguageRuntime {
			return Go{Environment: cfg.Environment, Cwd: cfg.Cwd, Env: cfg.Env}
		},
		LoweredRuntime: loweredRuntime("Go")})
	RegisterLanguage(Language{Canonical: "powershell", Aliases: []string{"pwsh", "ps1"}, NeedsEnvironment: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return PowerShell{Environment: cfg.Environment, Cwd: cfg.Cwd} },
		LoweredRuntime: loweredRuntime("PowerShell")})
	// C# compiles through the same pinned PowerShell (Add-Type); same-language
	// blocks carry #line directives so compiler diagnostics name the fence.
	RegisterLanguage(Language{Canonical: "csharp", Aliases: []string{"cs"}, NeedsEnvironment: true, LineDirectives: true,
		NewRuntime:     func(cfg RuntimeConfig) LanguageRuntime { return CSharp{Environment: cfg.Environment, Cwd: cfg.Cwd} },
		LoweredRuntime: loweredRuntime("CSharp")})
}
