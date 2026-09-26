// Package goldentest holds the golden-file mechanics the db/sql packages' tests share. Only
// _test.go files import it.
//
// Two golden files pin what Postgres and MySQL render: db/sql/core's testdata/conditions.golden
// for the condition family and db/sql/builder's testdata/pg_mysql.golden for whole builder
// queries. Both headers promise the same encoding of args — the dynamic type of every value,
// nil told apart from an empty slice — so the encoder, the comparison and the diff report live
// here once, where a fix to one cannot miss the other. It mirrors
// docs/app-template/test/testkit/golden.go, which lives in the app template's own module and so
// cannot be imported from here. The -update flag stays in each test package, which passes its
// value in, so `go test ./db/sql/core -run Golden -update` rewrites that package's file only.
package goldentest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Entry is one named case: the lines recorded for it, each ending in a newline.
type Entry struct {
	Name string
	Body string
}

// EncodeArgs spells out args so that nothing an executor could care about is lost. A nil
// slice and an empty one are different values ("nil" and "[]"), and each element carries its
// dynamic type, so int(1) is not int64(1) and a core.Identifier is not a string. Plain %#v
// would say `1` for both integers and `"users"` for both strings.
func EncodeArgs(args []any) string {
	if args == nil {
		return "nil"
	}
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = encodeArg(arg)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func encodeArg(arg any) string {
	if arg == nil {
		return "nil"
	}
	value := fmt.Sprintf("%#v", arg)
	typ := fmt.Sprintf("%T", arg)
	if strings.HasPrefix(value, typ) {
		return value
	}
	return typ + "(" + value + ")"
}

// AssertMatches compares entries, sorted by name, with the file at path; with update it
// rewrites the file instead. On a mismatch it names the first entry that differs and shows
// both sides, because a whole-file dump of a few hundred entries buries the one line that
// matters.
func AssertMatches(t testing.TB, path, header string, entries []Entry, update bool) {
	t.Helper()

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for i := 1; i < len(entries); i++ {
		if entries[i].Name == entries[i-1].Name {
			t.Fatalf("golden entry %q is defined twice; every case needs its own name so a diff says which one moved", entries[i].Name)
		}
	}
	got := format(header, entries)

	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date. Rerun with -update and review the diff.\n%s", path, describeDiff(string(want), got))
	}
}

func format(header string, entries []Entry) string {
	var sb strings.Builder
	sb.WriteString(header)
	for _, entry := range entries {
		sb.WriteString("\n=== " + entry.Name + "\n")
		sb.WriteString(entry.Body)
	}
	return sb.String()
}

// parse splits a golden file into its entries, keyed by name. Anything before the first "=== "
// line is the header and is not an entry.
func parse(content string) map[string]string {
	bodies := map[string]string{}
	name := ""
	var body strings.Builder
	flush := func() {
		if name != "" {
			bodies[name] = strings.TrimRight(body.String(), "\n") + "\n"
		}
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, "=== ") {
			flush()
			name = strings.TrimSuffix(strings.TrimPrefix(line, "=== "), "\n")
			body.Reset()
			continue
		}
		if name != "" {
			body.WriteString(line)
		}
	}
	flush()
	return bodies
}

func describeDiff(want, got string) string {
	wantBodies, gotBodies := parse(want), parse(got)

	names := make([]string, 0, len(wantBodies)+len(gotBodies))
	for name := range wantBodies {
		names = append(names, name)
	}
	for name := range gotBodies {
		if _, ok := wantBodies[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var changed, added, removed int
	first := ""
	for _, name := range names {
		w, inWant := wantBodies[name]
		g, inGot := gotBodies[name]
		var detail string
		switch {
		case inWant && inGot && w == g:
			continue
		case inWant && inGot:
			changed++
			detail = fmt.Sprintf("entry %q changed\n--- want\n%s--- got\n%s", name, w, g)
		case inGot:
			added++
			detail = fmt.Sprintf("entry %q is new\n--- got\n%s", name, g)
		default:
			removed++
			detail = fmt.Sprintf("entry %q is no longer produced\n--- want\n%s", name, w)
		}
		if first == "" {
			first = detail
		}
	}
	if first == "" {
		return "every entry matches; the difference is in the header or the layout between entries"
	}
	return fmt.Sprintf("%d changed, %d new, %d gone. First difference: %s", changed, added, removed, first)
}
