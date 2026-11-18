//go:build full

package lower_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

// Archived upstream Go-by-Example bytes are inputs to both compilers. This
// verifies native lowering; interpreter failures have their own retained replay.
func TestGoSourceGbENativeArtifacts(t *testing.T) {
	pinsBytes, err := os.ReadFile("testdata/gosource-gbe/sha256.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err = json.Unmarshal(pinsBytes, &pins); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recover", "structs", "xml", "generics", "range-over-iterators", "embed-directive", "logging", "unnamed"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/gosource-gbe/" + name + ".go.txt")
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != pins[name] {
				t.Fatal("original fixture bytes changed")
			}
			root := t.TempDir()
			originalDir := filepath.Join(root, "original")
			generatedDir := filepath.Join(root, "generated")
			for _, dir := range []string{originalDir, generatedDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "embed-directive" {
					if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
						t.Fatal(err)
					}
					files, err := os.ReadDir("testdata/gosource-gbe/folder")
					if err != nil {
						t.Fatal(err)
					}
					for _, file := range files {
						b, err := os.ReadFile("testdata/gosource-gbe/folder/" + file.Name())
						if err != nil {
							t.Fatal(err)
						}
						if fmt.Sprintf("%x", sha256.Sum256(b)) != pins["folder/"+file.Name()] {
							t.Fatal("original asset bytes changed")
						}
						if err := os.WriteFile(filepath.Join(dir, "folder", file.Name()), b, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			source := filepath.Join(originalDir, name+".go")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(bytes.NewReader(data), source, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var wire bytes.Buffer
			if err := typedjson.Encode(&wire, program.File); err != nil {
				t.Fatal(err)
			}
			decoded, err := typedjson.Decode(&wire)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(decoded.(*syntax.File), lower.Options{Origin: source})
			if err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(generatedDir, "generated.go")
			if err := os.WriteFile(generated, result.Source, 0600); err != nil {
				t.Fatal(err)
			}
			// Mirror the public marker contract: directives are nonempty lines
			// and therefore physical anchors; adjusted //line positions are not.
			pending, mapped := false, 0
			for lineIndex, line := range strings.Split(string(result.Source), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "// lower:") {
					pending = true
					continue
				}
				if pending && strings.TrimSpace(line) != "" {
					if mapped >= len(result.Mappings) {
						t.Fatal("missing generated mapping")
					}
					m := result.Mappings[mapped]
					if m.GoLine != lineIndex+1 || m.GoCol != len(line)-len(strings.TrimLeft(line, "\t "))+1 {
						t.Fatalf("physical marker mismatch: %#v", m)
					}
					mapped++
					pending = false
				}
			}
			if pending || mapped != len(result.Mappings) {
				t.Fatal("incomplete physical mappings")
			}
			for _, mapping := range result.Mappings {
				if mapping.GoLine < 1 || mapping.GoLine > bytes.Count(result.Source, []byte("\n"))+1 {
					t.Fatalf("invalid physical source map: %#v", mapping)
				}
			}
			originals := []string{source, generated}
			binaries := []string{filepath.Join(root, "oracle"), filepath.Join(root, "lowered")}
			for i, src := range originals {
				if out, err := exec.Command("go", "build", "-p", "2", "-o", binaries[i], src).CombinedOutput(); err != nil {
					t.Fatalf("build %s: %v\n%s", src, err, out)
				}
			}
			if after, err := os.ReadFile(source); err != nil || !bytes.Equal(after, data) {
				t.Fatal("original source changed")
			}
			if err := os.RemoveAll(originalDir); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(generatedDir); err != nil {
				t.Fatal(err)
			}
			streams := make([][2]string, 2)
			for i, binary := range binaries {
				var stdout, stderr bytes.Buffer
				cmd := exec.Command(binary)
				cmd.Env = []string{"PATH=", "TZ=UTC"}
				cmd.Dir = root
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("artifact %d: %v %s", i, err, stderr.String())
				}
				streams[i] = [2]string{stdout.String(), stderr.String()}
			}
			if name == "logging" {
				for i := range streams {
					if !strings.Contains(streams[i][1], "logging.go:40: with file/line") {
						t.Fatalf("lost original caller: %s", streams[i][1])
					}
					for j := range streams[i] {
						streams[i][j] = goSourceLogTimes.ReplaceAllString(streams[i][j], "<time>")
					}
				}
			}
			if streams[0] != streams[1] {
				t.Fatalf("native=%q\nlowered=%q", streams[0], streams[1])
			}
		})
	}
}

// Only volatile standard-log and RFC3339 timestamps are removed. Filenames,
// line numbers, JSON keys, messages, ordering and stream identity still compare.
var goSourceLogTimes = regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
