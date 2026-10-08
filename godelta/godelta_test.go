package godelta

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRowsWellFormed(t *testing.T) {
	rows, err := Rows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID] {
			t.Errorf("duplicate id %s", r.ID)
		}
		seen[r.ID] = true
		if !validSurface[r.Surface] {
			t.Errorf("%s: surface %q", r.ID, r.Surface)
		}
		if !validStatus[r.Status] {
			t.Errorf("%s: status %q", r.ID, r.Status)
		}
		if r.Spec == "" || r.Production == "" || r.Reason == "" || r.Fixture == "" {
			t.Errorf("%s: empty spec, production, reason or fixture", r.ID)
		}
		if r.Status != StatusSupported && r.Workaround == "" {
			t.Errorf("%s: %s row without a workaround", r.ID, r.Status)
		}
		if r.Status == StatusExcluded && r.Diagnostic == "" {
			t.Errorf("%s: excluded row without the diagnostic the engine prints", r.ID)
		}
	}
}

func TestLookupByIDAndByRefusalText(t *testing.T) {
	rows, _ := Rows()
	first := rows[0]
	if got := Lookup(strings.ToLower(first.ID)); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("lookup by id: %+v", got)
	}
	var withDiag Row
	for _, r := range rows {
		if r.Diagnostic != "" {
			withDiag = r
			break
		}
	}
	text := "script.bsh: line 3: " + withDiag.Diagnostic + " (trailing text)"
	found := false
	for _, r := range Lookup(text) {
		found = found || r.ID == withDiag.ID
	}
	if !found {
		t.Fatalf("a refusal containing %q did not resolve to %s", withDiag.Diagnostic, withDiag.ID)
	}
	if got := Lookup("no such refusal anywhere"); len(got) != 0 {
		t.Fatalf("unrelated text matched %+v", got)
	}
}

func TestHumanPageIsCurrent(t *testing.T) {
	want, err := os.ReadFile("../docs/go-delta.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := Markdown(); got != string(want) {
		t.Fatal("docs/go-delta.md is stale: run `go generate ./godelta`")
	}
}

func TestMachineRenderings(t *testing.T) {
	rows, _ := Rows()
	var out, errb bytes.Buffer
	if code := Main([]string{"--json"}, &out, &errb); code != 0 {
		t.Fatalf("json exit %d: %s", code, errb.String())
	}
	var decoded struct {
		Version int   `json:"version"`
		Rows    []Row `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != Version || len(decoded.Rows) != len(rows) {
		t.Fatalf("json version %d rows %d, want %d rows", decoded.Version, len(decoded.Rows), len(rows))
	}
	out.Reset()
	if code := Main([]string{"--tsv"}, &out, &errb); code != 0 || out.String() != string(tsvSource) {
		t.Fatalf("tsv rendering differs from the source of truth (exit %d)", code)
	}
	out.Reset()
	if code := Main([]string{rows[0].ID}, &out, &errb); code != 0 || !strings.Contains(out.String(), rows[0].Workaround) && rows[0].Workaround != "" {
		t.Fatalf("row detail: exit %d %q", code, out.String())
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"no such refusal anywhere"}, &out, &errb); code != 1 {
		t.Fatalf("unknown query exit %d, want 1", code)
	}
}
