// Command iotgen validates schema registry files and generates Go and C
// constants from them (spec 04 §8).
//
//	iotgen check spec/schemas/*.yaml
//	iotgen gen -c sdk/c/schemas -go internal/schemas spec/schemas/*.yaml
//
// Publishing to the registry (iotgen publish) and versioning checks against
// the previously published version are Phase 1.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Field struct {
	ID          int            `yaml:"id"`
	Key         string         `yaml:"key"`
	Type        string         `yaml:"type"`
	UnitIn      string         `yaml:"unit_in"`
	UnitOut     string         `yaml:"unit_out"`
	Range       []float64      `yaml:"range"`
	Precision   float64        `yaml:"precision"`
	Values      map[int]string `yaml:"values"`
	Agg         []string       `yaml:"agg"`
	Class       string         `yaml:"class"`
	Calibration []string       `yaml:"calibration"`
	Counter     bool           `yaml:"counter"`
	Deprecated  bool           `yaml:"deprecated"`
	Default     any            `yaml:"default"`
	Args        []Field        `yaml:"args"`
	Attrs       []Field        `yaml:"attrs"`
}

type Slot struct {
	Slot    int      `yaml:"slot"`
	Key     string   `yaml:"key"`
	Accepts []string `yaml:"accepts"`
}

type Schema struct {
	Schema   string  `yaml:"schema"`
	Kind     string  `yaml:"kind"`
	Version  string  `yaml:"version"`
	Title    string  `yaml:"title"`
	Channels []Field `yaml:"channels"`
	Slots    []Slot  `yaml:"slots"`
	Config   []Field `yaml:"config"`
	Commands []Field `yaml:"commands"`
	Events   []Field `yaml:"events"`
	path     string
}

var (
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	keyRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	types     = set("float", "int", "bool", "enum", "string", "geo")
	aggs      = set("avg", "min", "max", "sum", "last", "time_in_state")
	classes   = set("diagnostic", "telemetry", "location", "sensitive")
	kinds     = set("product", "module", "gateway", "sys")
	calMeth   = set("linear", "polynomial", "lut") // lut: Later
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func load(path string) (*Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schema
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.path = path
	return &s, nil
}

// check applies the structural rules of spec 04 §2-§3.
func check(s *Schema) []string {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf("%s: "+f, append([]any{s.path}, a...)...)) }
	if !nameRE.MatchString(s.Schema) {
		bad("schema name %q invalid", s.Schema)
	}
	if !kinds[s.Kind] {
		bad("kind %q invalid", s.Kind)
	}
	if !versionRE.MatchString(s.Version) {
		bad("version %q must be \"major.minor\" (quote it in YAML)", s.Version)
	}
	ids, keys := map[int]bool{}, map[string]bool{}
	for _, c := range s.Channels {
		lo, hi := 1, 32767
		if s.Kind == "sys" {
			lo, hi = 32768, 65535
		}
		if c.ID < lo || c.ID > hi {
			bad("channel %s id %d outside %d..%d", c.Key, c.ID, lo, hi)
		}
		if ids[c.ID] || keys[c.Key] {
			bad("channel %s/%d duplicated", c.Key, c.ID)
		}
		ids[c.ID], keys[c.Key] = true, true
		if !keyRE.MatchString(c.Key) {
			bad("channel key %q invalid", c.Key)
		}
		if !types[c.Type] {
			bad("channel %s type %q invalid", c.Key, c.Type)
		}
		if c.Type == "enum" && len(c.Values) == 0 {
			bad("enum channel %s has no values", c.Key)
		}
		if len(c.Agg) == 0 {
			bad("channel %s needs agg", c.Key)
		}
		for _, a := range c.Agg {
			if !aggs[a] {
				bad("channel %s agg %q invalid", c.Key, a)
			}
		}
		if !classes[c.Class] {
			bad("channel %s class %q invalid", c.Key, c.Class)
		}
		for _, m := range c.Calibration {
			if !calMeth[m] {
				bad("channel %s calibration %q invalid", c.Key, m)
			}
		}
		if len(c.Range) != 0 && (len(c.Range) != 2 || c.Range[0] > c.Range[1]) {
			bad("channel %s range invalid", c.Key)
		}
		if c.Type == "int" && len(c.Range) == 2 && (c.Range[0] < -(1<<53) || c.Range[1] > 1<<53) {
			bad("channel %s int range exceeds 2^53 (spec 04 §2.2)", c.Key)
		}
	}
	for _, sl := range s.Slots {
		if s.Kind != "product" {
			bad("only products have slots")
		}
		if sl.Slot < 1 || sl.Slot > 239 || !keyRE.MatchString(sl.Key) {
			bad("slot %d/%s invalid", sl.Slot, sl.Key)
		}
	}
	uniq := func(what string, fs []Field, lo, hi int) {
		seen := map[int]bool{}
		for _, f := range fs {
			if f.ID < lo || f.ID > hi || seen[f.ID] || !keyRE.MatchString(f.Key) {
				bad("%s %s/%d invalid or duplicated", what, f.Key, f.ID)
			}
			seen[f.ID] = true
		}
	}
	uniq("config", s.Config, 1, 65535)
	uniq("command", s.Commands, 1, 65535)
	uniq("event", s.Events, 1000, 65535)
	return errs
}

func ident(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

func genC(s *Schema) string {
	var b strings.Builder
	p := ident(s.Schema)
	maj := strings.Split(s.Version, ".")[0]
	fmt.Fprintf(&b, "/* Generated by iotgen from %s. Do not edit. */\n", filepath.Base(s.path))
	fmt.Fprintf(&b, "#ifndef IOTP_SCHEMA_%s_V%s_H\n#define IOTP_SCHEMA_%s_V%s_H\n\n", p, maj, p, maj)
	fmt.Fprintf(&b, "#define %s_SCHEMA \"%s@%s\"\n\n", p, s.Schema, s.Version)
	for _, c := range s.Channels {
		fmt.Fprintf(&b, "#define %s_CH_%s %du /* %s %s %s */\n", p, ident(c.Key), c.ID, c.Type, c.UnitIn, c.Class)
		vals := make([]int, 0, len(c.Values))
		for v := range c.Values {
			vals = append(vals, v)
		}
		sort.Ints(vals)
		for _, v := range vals {
			fmt.Fprintf(&b, "#define %s_%s_%s %du\n", p, ident(c.Key), ident(c.Values[v]), v)
		}
	}
	for _, sl := range s.Slots {
		fmt.Fprintf(&b, "#define %s_SLOT_%s %du\n", p, ident(sl.Key), sl.Slot)
	}
	for _, f := range s.Config {
		fmt.Fprintf(&b, "#define %s_CFG_%s ((0u << 16) | %du)\n", p, ident(f.Key), f.ID)
		if f.Default != nil {
			d := fmt.Sprint(f.Default)
			if f.Type == "bool" {
				d = map[string]string{"true": "1", "false": "0"}[d]
			}
			fmt.Fprintf(&b, "#define %s_CFG_%s_DEFAULT %s\n", p, ident(f.Key), d)
		}
	}
	for _, f := range s.Commands {
		fmt.Fprintf(&b, "#define %s_CMD_%s %du\n", p, ident(f.Key), f.ID)
		for _, a := range f.Args {
			fmt.Fprintf(&b, "#define %s_CMD_%s_ARG_%s %du\n", p, ident(f.Key), ident(a.Key), a.ID)
		}
	}
	for _, f := range s.Events {
		fmt.Fprintf(&b, "#define %s_EV_%s %du\n", p, ident(f.Key), f.ID)
		for _, a := range f.Attrs {
			fmt.Fprintf(&b, "#define %s_EV_%s_ATTR_%s %du\n", p, ident(f.Key), ident(a.Key), a.ID)
		}
	}
	b.WriteString("\n#endif\n")
	return b.String()
}

func goIdent(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	for i, x := range parts {
		parts[i] = strings.ToUpper(x[:1]) + x[1:]
	}
	return strings.Join(parts, "")
}

func genGo(ss []*Schema) string {
	var b strings.Builder
	b.WriteString("// Code generated by iotgen. DO NOT EDIT.\n\npackage schemas\n\n")
	for _, s := range ss {
		p := goIdent(s.Schema)
		fmt.Fprintf(&b, "// %s\nconst %sSchema = %q\n\nconst (\n", s.Title, p, s.Schema+"@"+s.Version)
		for _, c := range s.Channels {
			fmt.Fprintf(&b, "\t%sCh%s uint16 = %d\n", p, goIdent(c.Key), c.ID)
		}
		for _, sl := range s.Slots {
			fmt.Fprintf(&b, "\t%sSlot%s uint8 = %d\n", p, goIdent(sl.Key), sl.Slot)
		}
		for _, f := range s.Config {
			fmt.Fprintf(&b, "\t%sCfg%s uint32 = %d\n", p, goIdent(f.Key), f.ID)
		}
		for _, f := range s.Events {
			fmt.Fprintf(&b, "\t%sEv%s uint16 = %d\n", p, goIdent(f.Key), f.ID)
		}
		b.WriteString(")\n\n")
	}
	return b.String()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: iotgen check|gen [flags] files...")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cOut := fs.String("c", "", "C header output directory")
	goOut := fs.String("go", "", "Go package output directory")
	fs.Parse(os.Args[2:])

	var ss []*Schema
	failed := false
	for _, path := range fs.Args() {
		s, err := load(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
			continue
		}
		for _, e := range check(s) {
			fmt.Fprintln(os.Stderr, e)
			failed = true
		}
		ss = append(ss, s)
	}
	if failed {
		os.Exit(1)
	}
	switch cmd {
	case "check":
		fmt.Printf("%d schemas OK\n", len(ss))
	case "gen":
		if *cOut != "" {
			os.MkdirAll(*cOut, 0o755)
			for _, s := range ss {
				name := fmt.Sprintf("%s_v%s.h", s.Schema, strings.Split(s.Version, ".")[0])
				if err := os.WriteFile(filepath.Join(*cOut, name), []byte(genC(s)), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
		if *goOut != "" {
			os.MkdirAll(*goOut, 0o755)
			if err := os.WriteFile(filepath.Join(*goOut, "schemas_gen.go"), []byte(genGo(ss)), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}
