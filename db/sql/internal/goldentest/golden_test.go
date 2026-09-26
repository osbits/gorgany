package goldentest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type column string

// TestEncodeArgsKeepsWhatAnExecutorSees: both golden headers promise that nil and an empty
// slice differ, and that every value carries its dynamic type.
func TestEncodeArgsKeepsWhatAnExecutorSees(t *testing.T) {
	assert.Equal(t, "nil", EncodeArgs(nil))
	assert.Equal(t, "[]", EncodeArgs([]any{}))
	assert.Equal(t, `[int(1), int64(1), string("users"), goldentest.column("users"), nil]`,
		EncodeArgs([]any{1, int64(1), "users", column("users"), nil}))
}

// TestAssertMatchesWritesThenCompares: update writes the entries sorted by name under the
// header, and a later comparison against that file passes.
func TestAssertMatchesWritesThenCompares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testdata", "x.golden")
	entries := []Entry{{Name: "b", Body: "sql:  \"b\"\n"}, {Name: "a", Body: "sql:  \"a\"\n"}}

	AssertMatches(t, path, "# header\n", entries, true)
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# header\n\n=== a\nsql:  \"a\"\n\n=== b\nsql:  \"b\"\n", string(written))

	AssertMatches(t, path, "# header\n", []Entry{{Name: "a", Body: "sql:  \"a\"\n"}, {Name: "b", Body: "sql:  \"b\"\n"}}, false)
}

// TestDescribeDiffNamesTheFirstEntryThatMoved: a mismatch report leads with one entry, not the
// whole file.
func TestDescribeDiffNamesTheFirstEntryThatMoved(t *testing.T) {
	want := format("# h\n", []Entry{{Name: "a", Body: "x\n"}, {Name: "b", Body: "y\n"}, {Name: "c", Body: "z\n"}})
	got := format("# h\n", []Entry{{Name: "a", Body: "x\n"}, {Name: "b", Body: "Y\n"}, {Name: "d", Body: "w\n"}})

	assert.Equal(t, "1 changed, 1 new, 1 gone. First difference: entry \"b\" changed\n--- want\ny\n--- got\nY\n",
		describeDiff(want, got))
	assert.Equal(t, "every entry matches; the difference is in the header or the layout between entries",
		describeDiff("# one\n"+want[len("# h\n"):], want))
}
