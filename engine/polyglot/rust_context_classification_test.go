package polyglot

import (
	"encoding/json"
	"testing"
)

func TestRustContextUndeclaredType(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(map[string]any, map[string]any, map[string]any)
	}{
		{"authored-type", "RUST-ESOURCE", nil},
		{"other-identifier", "RUST-ESOURCE", func(m, s, r map[string]any) {
			m["message"] = "cannot find type `Other_Type2` in this scope"
			s["label"] = "use of undeclared type `Other_Type2`"
		}},
		{"unlinked-crate", "RUST-EDEPENDENCY", func(m, s, r map[string]any) {
			m["message"] = "failed to resolve: use of unresolved module or unlinked crate `ThreadPool`"
		}},
		{"module", "RUST-EDEPENDENCY", func(m, s, r map[string]any) { m["message"] = "cannot find module `ThreadPool` in this scope" }},
		{"import", "RUST-EDEPENDENCY", func(m, s, r map[string]any) { m["code"] = map[string]string{"code": "E0432"} }},
		{"dependency-target", "RUST-EDEPENDENCY", func(m, s, r map[string]any) { r["target"] = map[string]string{"src_path": "/vendor/lib.rs"} }},
		{"mismatched-label", "RUST-EDEPENDENCY", func(m, s, r map[string]any) { s["label"] = "use of undeclared type `Other`" }},
		{"missing-label", "RUST-EDEPENDENCY", func(m, s, r map[string]any) { delete(s, "label") }},
		{"non-identifier", "RUST-EDEPENDENCY", func(m, s, r map[string]any) {
			m["message"] = "cannot find type `a::B` in this scope"
			s["label"] = "use of undeclared type `a::B`"
		}},
		{"outside-file", "RUST-EBUILD", func(m, s, r map[string]any) { s["file_name"] = "src/other.rs" }},
		{"generated", "RUST-EBUILD", func(m, s, r map[string]any) { s["byte_end"] = 101 }},
		{"negative-start", "RUST-EBUILD", func(m, s, r map[string]any) { s["byte_start"] = -1 }},
		{"missing-start", "RUST-EBUILD", func(m, s, r map[string]any) { delete(s, "byte_start") }},
		{"reversed", "RUST-EBUILD", func(m, s, r map[string]any) { s["byte_start"] = 11 }},
		{"empty", "RUST-EBUILD", func(m, s, r map[string]any) { s["byte_start"] = 10 }},
		{"non-primary", "RUST-EBUILD", func(m, s, r map[string]any) { s["is_primary"] = false }},
		{"macro-expansion", "RUST-EBUILD", func(m, s, r map[string]any) { s["expansion"] = map[string]any{"macro_decl_name": "generated!"} }},
		{"mixed-primary", "RUST-EBUILD", func(m, s, r map[string]any) {
			m["spans"] = []any{s, map[string]any{"file_name": "src/main.rs", "is_primary": true, "byte_start": 101, "byte_end": 110}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := map[string]any{"file_name": "src/main.rs", "byte_start": 0, "byte_end": 10, "is_primary": true, "expansion": nil, "label": "use of undeclared type `ThreadPool`"}
			m := map[string]any{"level": "error", "code": map[string]string{"code": "E0433"}, "message": "cannot find type `ThreadPool` in this scope", "spans": []any{s}}
			r := map[string]any{"reason": "compiler-message", "target": map[string]string{"src_path": "/work/src/main.rs"}, "message": m}
			if tc.change != nil {
				tc.change(m, s, r)
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if got := rustBuildFailureClass(b, "/work/src/main.rs", 100); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestRustContextUndeclaredTypeMixedFailure(t *testing.T) {
	source := `{"reason":"compiler-message","target":{"src_path":"/work/src/main.rs"},"message":{"level":"error","code":{"code":"E0433"},"message":"cannot find type ` + "`Pool`" + ` in this scope","spans":[{"file_name":"src/main.rs","byte_start":0,"byte_end":4,"is_primary":true,"label":"use of undeclared type ` + "`Pool`" + `"}]}}`
	for _, bad := range []string{`{"reason":"compiler-message",`, `{"reason":"compiler-message","message":{"spans":"bad"}}`} {
		for _, data := range []string{source + "\n" + bad, bad + "\n" + source} {
			if got := rustBuildFailureClass([]byte(data), "/work/src/main.rs", 100); got != "RUST-EBUILD" {
				t.Fatal(got)
			}
		}
	}
	dependency := `{"reason":"compiler-message","target":{"src_path":"/vendor/lib.rs"},"message":{"level":"error","code":{"code":"E0433"}}}`
	for _, data := range []string{source + "\n" + dependency, dependency + "\n" + source} {
		if got := rustBuildFailureClass([]byte(data), "/work/src/main.rs", 100); got != "RUST-EDEPENDENCY" {
			t.Fatal(got)
		}
	}
}
