package polyglot

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// aggregateGoSource maps each authored fence line without moving its body.
// The generated package clause, when needed, stays outside this mapping.
func aggregateGoSource(blocks []Block) string {
	var out strings.Builder
	for _, block := range blocks {
		name := block.Filename
		if name == "" {
			name = "<bash++ go>"
		}
		line := block.Line
		if line < 1 {
			line = 1
		}
		fmt.Fprintf(&out, "//line %s:%d\n%s", name, line, block.Source)
		if !strings.HasSuffix(block.Source, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// goEmbedSourceDir refuses to guess one source parent when separate authored
// blocks declare assets from different parents. An absent origin leaves the
// environment's discovered source directory as the explicit fallback.
func goEmbedSourceDir(blocks []Block) (string, error) {
	var dir string
	missing := false
	for _, block := range blocks {
		if !strings.Contains(block.Source, "//go:embed ") {
			continue
		}
		if block.Origin == "" {
			missing = true
			continue
		}
		origin, err := filepath.Abs(block.Origin)
		if err != nil {
			return "", err
		}
		parent := filepath.Dir(origin)
		if dir != "" && dir != parent {
			return "", fmt.Errorf("Go embed blocks have different source directories")
		}
		dir = parent
	}
	if missing && dir != "" {
		return "", fmt.Errorf("Go embed blocks have incomplete source origins")
	}
	return dir, nil
}

const (
	maxGoEmbedFiles   = 256
	maxGoEmbedBytes   = 16 << 20
	maxGoEmbedEntries = 4096
)

// stageGoEmbedAssets binds declared embed patterns to exact bytes at analysis
// time. Physical copies are private; virtual names retain the source-relative
// hierarchy that cmd/go uses to resolve directives.
func stageGoEmbedAssets(source, sourceDir, virtualDir, scratch string) (map[string]string, error) {
	patterns, err := goEmbedPatterns(source)
	if err != nil || len(patterns) == 0 {
		return nil, err
	}
	if sourceDir == "" {
		return nil, fmt.Errorf("Go embed source directory is required")
	}
	root, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	entries := 0
	matched := 0
	var visit func(string, []string, bool, bool) error
	visit = func(rel string, parts []string, all, recursive bool) error {
		if len(parts) == 0 {
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			if !info.IsDir() {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("Go embed asset is not a regular file: %s", rel)
				}
				selected[rel] = true
				matched++
				return nil
			}
			// A direct directory match is an error at a nested module boundary.
			// During a directory walk, cmd/go skips that subtree instead.
			if rel != "." {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "go.mod")); err == nil {
					if recursive {
						return nil
					}
					return fmt.Errorf("Go embed directory %s is in a different module", rel)
				}
			}
			children, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			entries += len(children)
			if entries > maxGoEmbedEntries {
				return fmt.Errorf("Go embed directory entry limit exceeded")
			}
			for _, child := range children {
				if !all && (strings.HasPrefix(child.Name(), ".") || strings.HasPrefix(child.Name(), "_")) {
					continue
				}
				if err := visit(path.Join(rel, child.Name()), nil, all, true); err != nil {
					return err
				}
			}
			return nil
		}
		children, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		entries += len(children)
		if entries > maxGoEmbedEntries {
			return fmt.Errorf("Go embed directory entry limit exceeded")
		}
		for _, child := range children {
			match, err := path.Match(parts[0], child.Name())
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if child.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("Go embed asset is a symlink: %s", path.Join(rel, child.Name()))
			}
			if child.IsDir() {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Join(rel, child.Name())), "go.mod")); err == nil {
					return fmt.Errorf("Go embed directory %s is in a different module", path.Join(rel, child.Name()))
				}
			}
			if err := visit(path.Join(rel, child.Name()), parts[1:], all, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, declaration := range patterns {
		pattern := declaration
		all := false
		if strings.HasPrefix(pattern, "all:") {
			pattern, all = strings.TrimPrefix(pattern, "all:"), true
		}
		if pattern == "" || path.IsAbs(pattern) || strings.Contains(pattern, "\\") || path.Clean(pattern) != pattern || strings.HasPrefix(pattern, "../") || pattern == ".." {
			return nil, fmt.Errorf("invalid Go embed pattern %q", declaration)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("invalid Go embed pattern %q", declaration)
			}
		}
		before := matched
		if err := visit(".", strings.Split(pattern, "/"), all, false); err != nil {
			return nil, fmt.Errorf("Go embed pattern %q: %w", declaration, err)
		}
		if matched == before {
			return nil, fmt.Errorf("Go embed pattern %q matched no files", declaration)
		}
	}
	if len(selected) > maxGoEmbedFiles {
		return nil, fmt.Errorf("Go embed file limit exceeded")
	}
	result := make(map[string]string, len(selected))
	total := 0
	for rel := range selected {
		original := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(original)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Go embed asset is not a regular file: %s", rel)
		}
		data, err := os.ReadFile(original)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxGoEmbedBytes {
			return nil, fmt.Errorf("Go embed byte limit exceeded")
		}
		backing := filepath.Join(scratch, "assets", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(backing), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(backing, data, 0o600); err != nil {
			return nil, err
		}
		result[filepath.Join(virtualDir, filepath.FromSlash(rel))] = backing
	}
	return result, nil
}

func goEmbedPatterns(source string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "fence.go", source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		return nil, err
	}
	var patterns []string
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, "//go:embed ") {
				continue
			}
			parsed, err := splitGoEmbedPatterns(strings.TrimPrefix(comment.Text, "//go:embed "))
			if err != nil {
				return nil, err
			}
			patterns = append(patterns, parsed...)
		}
	}
	return patterns, nil
}

func splitGoEmbedPatterns(text string) ([]string, error) {
	var out []string
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		if text[0] == '"' || text[0] == '`' {
			quote := text[0]
			end := 1
			for end < len(text) {
				if text[end] == '\\' && quote == '"' {
					end += 2
					continue
				}
				if text[end] == quote {
					break
				}
				end++
			}
			if end >= len(text) {
				return nil, fmt.Errorf("unterminated Go embed pattern")
			}
			value, err := strconv.Unquote(text[:end+1])
			if err != nil {
				return nil, err
			}
			out = append(out, value)
			text = text[end+1:]
			continue
		}
		end := strings.IndexAny(text, " \t")
		if end < 0 {
			end = len(text)
		}
		out = append(out, text[:end])
		text = text[end:]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty Go embed directive")
	}
	return out, nil
}
