package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustLoad(t *testing.T, path string) *Schema {
	t.Helper()
	s, err := load(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// spec 04 §2-§3: the shipped schemas pass every structural rule.
func TestShippedSchemasPass(t *testing.T) {
	paths, err := filepath.Glob("../../spec/schemas/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no schemas found: %v", err)
	}
	if _, err := loadAll(paths); err != nil {
		t.Fatal(err)
	}
}

// spec 04 §2.2, §3: each rule rejects a violation and names it.
func TestCheckRejects(t *testing.T) {
	cases := map[string]struct {
		mut  func(s *Schema)
		want string
	}{
		"bad name":        {func(s *Schema) { s.Schema = "Env" }, "schema name"},
		"bad kind":        {func(s *Schema) { s.Kind = "widget" }, "kind"},
		"unquoted ver":    {func(s *Schema) { s.Version = "3" }, "major.minor"},
		"id out of range": {func(s *Schema) { s.Channels[0].ID = 40000 }, "outside 1..32767"},
		"dup id":          {func(s *Schema) { s.Channels[1].ID = s.Channels[0].ID }, "duplicated"},
		"bad type":        {func(s *Schema) { s.Channels[0].Type = "double" }, "type"},
		"enum no values":  {func(s *Schema) { s.Channels[0].Type = "enum"; s.Channels[0].Values = nil }, "no values"},
		"no agg":          {func(s *Schema) { s.Channels[0].Agg = nil }, "needs agg"},
		"bad agg":         {func(s *Schema) { s.Channels[0].Agg = []string{"median"} }, "agg"},
		"bad class":       {func(s *Schema) { s.Channels[0].Class = "secret" }, "class"},
		"bad cal":         {func(s *Schema) { s.Channels[0].Calibration = []string{"magic"} }, "calibration"},
		"bad range":       {func(s *Schema) { s.Channels[0].Range = []float64{5, 1} }, "range invalid"},
		"int > 2^53":      {func(s *Schema) { s.Channels[0].Type = "int"; s.Channels[0].Range = []float64{0, 1 << 54} }, "2^53"},
		"slot range":      {func(s *Schema) { s.Slots = []Slot{{Slot: 240, Key: "x"}} }, "slot 240"},
		"slot on module":  {func(s *Schema) { s.Kind = "module"; s.Slots = []Slot{{Slot: 1, Key: "x"}} }, "only products"},
		"event id low":    {func(s *Schema) { s.Events = []Field{{ID: 5, Key: "x"}} }, "event x/5"},
		"dup config":      {func(s *Schema) { s.Config = []Field{{ID: 1, Key: "a"}, {ID: 1, Key: "b"}} }, "config b/1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := mustLoad(t, "../../spec/schemas/envsense.yaml")
			c.mut(s)
			errs := strings.Join(check(s), "\n")
			if !strings.Contains(errs, c.want) {
				t.Fatalf("want a problem containing %q, got %q", c.want, errs)
			}
		})
	}
}

// spec 04 §2.2: sys schemas use the 32768..65535 channel range.
func TestSysChannelRange(t *testing.T) {
	s := mustLoad(t, "../../spec/schemas/envsense.yaml")
	s.Kind = "sys"
	s.Slots = nil
	if errs := strings.Join(check(s), "\n"); !strings.Contains(errs, "outside 32768..65535") {
		t.Fatalf("got %q", errs)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.yaml")
	if err := os.WriteFile(p, []byte("schema: x\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(p); err == nil {
		t.Fatal("unknown YAML keys must be rejected")
	}
	if _, err := load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file")
	}
}

// Generated output is deterministic and matches the committed files (spec 04 §5).
func TestGenerateMatchesCommitted(t *testing.T) {
	ss, err := loadAll([]string{"../../spec/schemas/co2.yaml", "../../spec/schemas/envsense.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = generate(ss, dir, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "envsense_v3.h")) //nolint:gosec // G304: file in the test's own temp dir.
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../sdk/c/schemas/envsense_v3.h")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("generated C header differs from sdk/c/schemas/envsense_v3.h; run make schemas")
	}
}

func TestRunCommands(t *testing.T) {
	if err := run("check", []string{"../../spec/schemas/envsense.yaml"}); err != nil {
		t.Fatal(err)
	}
	if err := run("frobnicate", nil); err == nil {
		t.Fatal("unknown command")
	}
	if err := run("check", []string{"-nope"}); err == nil {
		t.Fatal("bad flag")
	}
}
