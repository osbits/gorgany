package model

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tagFixture struct {
	Plain         string
	Named         string `json:"named"`
	Skipped       string `json:"-"`
	LiteralDash   string `json:"-,"`
	Omitted       string `json:"omitted,omitempty"`
	OptionsOnly   string `json:",omitempty"`
	SchemeOnly    string `scheme:"scheme_only"`
	BothTags      string `json:"from_json" scheme:"from_scheme"`
	SchemeSkipped string `scheme:"-"`
	EmptyTag      string `json:""`
}

func field(t *testing.T, name string) reflect.StructField {
	t.Helper()
	f, ok := reflect.TypeOf(tagFixture{}).FieldByName(name)
	require.True(t, ok)
	return f
}

func TestParseStructTag(t *testing.T) {
	tests := map[string]StructTag{
		"Plain":       {Name: "Plain"},
		"Named":       {Name: "named", HasName: true},
		"Skipped":     {Name: "Skipped", Skip: true},
		"LiteralDash": {Name: "-", HasName: true},
		"Omitted":     {Name: "omitted", HasName: true, OmitEmpty: true},
		"OptionsOnly": {Name: "OptionsOnly", OmitEmpty: true},
		"EmptyTag":    {Name: "EmptyTag"},
	}

	for name, want := range tests {
		assert.Equalf(t, want, ParseStructTag(field(t, name), JSONTagName), "field %s", name)
	}
}

// TestParseStructTagWorksForAnyTagName: the parser is shared between the json and
// scheme tags rather than duplicated per tag, which is what keeps their handling from
// drifting.
func TestParseStructTagWorksForAnyTagName(t *testing.T) {
	assert.Equal(t, StructTag{Name: "scheme_only", HasName: true},
		ParseStructTag(field(t, "SchemeOnly"), SchemeTagName))
	assert.Equal(t, StructTag{Name: "SchemeOnly"},
		ParseStructTag(field(t, "SchemeOnly"), JSONTagName),
		"a scheme tag is invisible to the json parse")
	assert.Equal(t, StructTag{Name: "SchemeSkipped", Skip: true},
		ParseStructTag(field(t, "SchemeSkipped"), SchemeTagName))
}

// TestWireFieldName is what validation errors report, so a client can map an error back
// to the field it sent.
func TestWireFieldName(t *testing.T) {
	tests := map[string]string{
		"Named":         "named",
		"SchemeOnly":    "scheme_only",
		"BothTags":      "from_json",
		"Plain":         "Plain",
		"OptionsOnly":   "OptionsOnly",
		"Skipped":       "Skipped",
		"SchemeSkipped": "SchemeSkipped",
		"LiteralDash":   "-",
		"EmptyTag":      "EmptyTag",
	}

	for name, want := range tests {
		assert.Equalf(t, want, WireFieldName(field(t, name)), "field %s", name)
	}
}

// TestJsonWinsOverScheme: a DTO carrying both binds from a JSON body by its json tag, so
// that is the name a client is most likely to have used.
func TestJsonWinsOverScheme(t *testing.T) {
	assert.Equal(t, "from_json", WireFieldName(field(t, "BothTags")))
}

// TestASkippedFieldFallsBackToItsGoName. `json:"-"` means the field has no wire name at
// all, and it can still carry validate rules — something has to be reported when one
// fails, and the Go name is the only honest answer.
func TestASkippedFieldFallsBackToItsGoName(t *testing.T) {
	assert.Equal(t, "Skipped", WireFieldName(field(t, "Skipped")))
}
