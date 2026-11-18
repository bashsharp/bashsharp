package gosource

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

const (
	preparedCacheDirEnv = "BASHPP_REEXEC_PREPARED_CACHE"
	preparedCacheIDEnv  = "BASHPP_REEXEC_INTERPRETER_ID"
	preparedCacheFormat = "gosource-prepared-v1"
)

type preparedProgramRecord struct {
	Format        string
	Key           string
	File          json.RawMessage
	Package       string
	Sources       []SourceInfo
	InitFunctions []string
	Main          string
	Resolutions   []Resolution
	Packages      []LinkedPackage
}

type preparedProgramCache struct {
	key, path, lock string
	record          *preparedProgramRecord
	owned           bool
	stop, stopped   chan struct{}
}

// openPreparedProgramCache is deliberately enabled only by a self-reexec
// launcher. Ordinary loads retain their in-process behavior. The launcher's
// private directory bounds the cache to one authenticated parent program; its
// interpreter ID and every input byte participate in the key.
func openPreparedProgramCache(sources []Source, options Options) (*preparedProgramCache, error) {
	// Reexec children are executable interpreted programs. Keep semantic-only
	// and native-unit loads on their ordinary path, where callers may consume
	// Program.Importer for later lowering.
	if !options.RunMain || options.PreserveNativeInit {
		return nil, nil
	}
	dir, interpreterID := os.Getenv(preparedCacheDirEnv), os.Getenv(preparedCacheIDEnv)
	if dir == "" || interpreterID == "" {
		return nil, nil
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("gosource: prepared cache directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("gosource: create prepared cache: %w", err)
	}
	key, ok := preparedProgramKey(interpreterID, sources, options)
	if !ok {
		// An input the key cannot authenticate disables the cache for this
		// load rather than risking a hit on stale content.
		return nil, nil
	}
	cache := &preparedProgramCache{
		key:  key,
		path: filepath.Join(dir, key+".json"),
		lock: filepath.Join(dir, key+".lock"),
	}
	for {
		if record := readPreparedProgram(cache.path, key); record != nil {
			cache.record = record
			return cache, nil
		}
		lock, err := os.OpenFile(cache.lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_ = lock.Close()
			cache.owned = true
			cache.stop, cache.stopped = make(chan struct{}), make(chan struct{})
			go cache.keepLockFresh()
			return cache, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("gosource: lock prepared cache: %w", err)
		}
		if info, statErr := os.Stat(cache.lock); statErr == nil && time.Since(info.ModTime()) > 15*time.Minute {
			_ = os.Remove(cache.lock)
			continue
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func preparedProgramKey(interpreterID string, sources []Source, options Options) (string, bool) {
	h := sha256.New()
	write := func(text string) {
		h.Write([]byte(text))
		h.Write([]byte{0})
	}
	write(preparedCacheFormat)
	write(interpreterID)
	goos, goarch := os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	write(goos)
	write(goarch)
	write(os.Getenv("GOEXPERIMENT"))
	toolchainEnv := []string{"GOROOT", "GOTOOLCHAIN", "GOFLAGS", "CGO_ENABLED", "BASHPP_GO"}
	if filepath.IsAbs(strings.TrimSpace(os.Getenv("BASHPP_GO"))) {
		// An absolute BASHPP_GO is the embedder's exact SDK selection; the
		// resolver returns it before consulting GOROOT. Reexec launchers use
		// this route while preserving the interpreted program's GOROOT, which
		// cmd/compile's TestScript replaces with a different testgoroot for
		// every script. Those roots affect the running compiler, not how these
		// identical sources are prepared, so including them here defeats the
		// launcher's shared cache and prepares the test main once per child.
		toolchainEnv = toolchainEnv[1:]
	}
	for _, name := range toolchainEnv {
		write(os.Getenv(name))
	}
	optionsKey, _ := json.Marshal(struct {
		RunMain, PreserveNativeInit, FakeImportC, TestBuiltins, CheckerBranchErrors, CheckAfterSyntaxErrors, GoTypesParserDiagnostics, TestMain bool
		GoVersion, ImportBase, ImportPath                                                                                                       string
	}{
		RunMain: options.RunMain, PreserveNativeInit: options.PreserveNativeInit,
		FakeImportC: options.FakeImportC, TestBuiltins: options.TestBuiltins,
		CheckerBranchErrors: options.CheckerBranchErrors, CheckAfterSyntaxErrors: options.CheckAfterSyntaxErrors,
		GoTypesParserDiagnostics: options.GoTypesParserDiagnostics, TestMain: options.TestMain,
		GoVersion: options.GoVersion, ImportBase: options.ImportBase, ImportPath: options.ImportPath,
	})
	h.Write(optionsKey)
	write("")
	for _, source := range sources {
		write(source.Name)
		digest := sha256.Sum256(source.Data)
		h.Write(digest[:])
	}
	for _, pkg := range options.Packages {
		write(pkg.Path)
		write(pkg.SourceDir)
		// A companion is a non-Go input the program links, so its bytes are
		// as much a part of the prepared program as a Go source's are.
		for _, companion := range pkg.CompanionFiles {
			write(companion)
			path := companion
			if !filepath.IsAbs(path) {
				path = filepath.Join(pkg.SourceDir, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", false
			}
			digest := sha256.Sum256(data)
			h.Write(digest[:])
		}
		pkgSources := append([]Source(nil), pkg.Sources...)
		sort.SliceStable(pkgSources, func(i, j int) bool { return pkgSources[i].Name < pkgSources[j].Name })
		for _, source := range pkgSources {
			write(source.Name)
			digest := sha256.Sum256(source.Data)
			h.Write(digest[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func readPreparedProgram(path, key string) *preparedProgramRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var record preparedProgramRecord
	if json.Unmarshal(data, &record) != nil || record.Format != preparedCacheFormat || record.Key != key {
		return nil
	}
	return &record
}

func (c *preparedProgramCache) program() *Program {
	if c == nil || c.record == nil {
		return nil
	}
	node, err := typedjson.Decode(bytes.NewReader(c.record.File))
	if err != nil {
		return nil
	}
	file, ok := node.(*syntax.File)
	if !ok || !file.GoSource {
		return nil
	}
	return &Program{
		File: file, Package: c.record.Package, Sources: c.record.Sources,
		InitFunctions: c.record.InitFunctions, Main: c.record.Main,
		Resolutions: c.record.Resolutions, Packages: c.record.Packages,
	}
}

func (c *preparedProgramCache) store(program *Program) error {
	if c == nil || !c.owned || program == nil || program.File == nil {
		return nil
	}
	var file bytes.Buffer
	if err := typedjson.Encode(&file, program.File); err != nil {
		return fmt.Errorf("gosource: encode prepared program: %w", err)
	}
	record := preparedProgramRecord{
		Format: preparedCacheFormat, Key: c.key,
		File: file.Bytes(), Package: program.Package, Sources: program.Sources,
		InitFunctions: program.InitFunctions, Main: program.Main,
		Resolutions: program.Resolutions, Packages: program.Packages,
	}
	data, err := json.Marshal(&record)
	if err != nil {
		return fmt.Errorf("gosource: encode prepared cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".prepared-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, c.path)
	}
	if err != nil {
		return fmt.Errorf("gosource: publish prepared cache: %w", err)
	}
	return nil
}

func (c *preparedProgramCache) close() {
	if c != nil && c.owned {
		close(c.stop)
		<-c.stopped
		_ = os.Remove(c.lock)
		c.owned = false
	}
}

func (c *preparedProgramCache) keepLockFresh() {
	defer close(c.stopped)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			_ = os.Chtimes(c.lock, now, now)
		case <-c.stop:
			return
		}
	}
}
