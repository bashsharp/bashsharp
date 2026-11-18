package polyglot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RustDiagnosticCapture opts into bounded evidence retention, never classification.
type RustDiagnosticCapture struct {
	Schema   int  `json:"schema"`
	LongType bool `json:"long_type"`
}

// RustDiagnosticAttachment is immutable evidence of a compiler-referenced file.
// Bytes is encoded as base64 by encoding/json; Path retains the compiler spelling.
type RustDiagnosticAttachment struct {
	Schema       string `json:"schema"`
	SourceSHA256 string `json:"source_sha256"`
	WorkerSHA256 string `json:"worker_sha256"`
	Worker       string `json:"worker_name"`
	Path         string `json:"original_path"`
	SHA256       string `json:"sha256"`
	Bytes        []byte `json:"bytes_b64"`
}

type rustCaptureMessage struct {
	Message string `json:"message"`
	Spans   []struct {
		FileName  string `json:"file_name"`
		ByteStart int    `json:"byte_start"`
		ByteEnd   int    `json:"byte_end"`
		Primary   bool   `json:"is_primary"`
	} `json:"spans"`
	Children []rustCaptureMessage `json:"children"`
}

var rustLongTypeNote = regexp.MustCompile("^the full name for the type has been written to ['`](.+)['`]$")
var rustCaptureWorker = regexp.MustCompile(`^m[0-9a-f]{16}$`)

func captureRustDiagnostics(c RustDependencyContext, source, generated, worker, target, root string, out []byte) error {
	if c.DiagnosticCapture == nil {
		return nil
	}
	if c.DiagnosticCapture.Schema != 1 || !c.DiagnosticCapture.LongType || !filepath.IsAbs(c.CacheDir) || rustSHA([]byte(source)) != c.SourceSHA256 || !rustCaptureWorker.MatchString(worker) {
		return fmt.Errorf("invalid capture identity")
	}
	attachments := map[string]RustDiagnosticAttachment{}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		var record struct {
			Reason string `json:"reason"`
			Target struct {
				SrcPath string `json:"src_path"`
			} `json:"target"`
			Message rustCaptureMessage `json:"message"`
		}
		if json.Unmarshal(line, &record) != nil || record.Reason != "compiler-message" {
			continue
		}
		bound := false
		if record.Target.SrcPath == root {
			for _, span := range record.Message.Spans {
				if span.Primary && (span.FileName == root || span.FileName == "src/main.rs") && span.ByteStart >= 0 && span.ByteEnd > span.ByteStart && span.ByteEnd <= len(source) {
					bound = true
				}
			}
		}
		var visit func(rustCaptureMessage) error
		visit = func(m rustCaptureMessage) error {
			if strings.Contains(m.Message, ".long-type-") {
				match := rustLongTypeNote.FindStringSubmatch(m.Message)
				if !bound || match == nil {
					return fmt.Errorf("unbound long-type reference")
				}
				path := match[1]
				base := filepath.Base(path)
				valid := regexp.MustCompile(`^` + regexp.QuoteMeta(worker) + `-[0-9a-f]+\.long-type-[0-9]+\.txt$`)
				if !valid.MatchString(base) || path != filepath.Join(target, "debug", "deps", base) {
					return fmt.Errorf("invalid long-type path")
				}
				data, err := rustCaptureRead(target, filepath.Join("debug", "deps", base))
				if err != nil {
					return err
				}
				attachments[base] = RustDiagnosticAttachment{"rust-long-type/v1", c.SourceSHA256, rustSHA([]byte(generated)), worker, path, rustSHA(data), data}
				if len(attachments) > 64 {
					return fmt.Errorf("too many diagnostic attachments")
				}
			}
			for _, child := range m.Children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(record.Message); err != nil {
			return err
		}
	}
	if len(attachments) == 0 {
		return nil
	}
	info, err := os.Lstat(c.CacheDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe cache directory")
	}
	cache, err := os.OpenRoot(c.CacheDir)
	if err != nil {
		return err
	}
	defer cache.Close()
	rel := filepath.Join("diagnostics", c.SourceSHA256, worker)
	for _, part := range []string{"diagnostics", filepath.Join("diagnostics", c.SourceSHA256), rel} {
		if err := cache.Mkdir(part, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := cache.Lstat(part)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe diagnostic directory")
		}
	}
	for base, attachment := range attachments {
		data, err := json.Marshal(attachment)
		if err != nil {
			return err
		}
		f, err := cache.OpenFile(filepath.Join(rel, base+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func rustCaptureRead(target, rel string) ([]byte, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe target directory")
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, part := range []string{"debug", filepath.Join("debug", "deps"), rel} {
		info, err := root.Lstat(part)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in diagnostic path")
		}
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, fmt.Errorf("diagnostic is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("diagnostic exceeds 64 KiB")
	}
	return data, err
}
