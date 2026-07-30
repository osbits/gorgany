package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F4: the envelope marshaller's stated goal is to serialise a DTO the way encoding/json
// would. These tests assert that directly — marshal the same struct both ways and compare —
// rather than asserting a hand-written expectation, because a hand-written expectation is
// exactly what drifted from the goal twice already.
//
// Where the marshaller deliberately differs, it says so and the difference is pinned
// separately at the bottom of this file. Everything else is parity.

// envelopeBody marshals v through the API envelope and returns just the body.
func envelopeBody(t *testing.T, v any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(&ApiReturnObject{HttpStatus: core.SuccessHttpStatus, Body: v})
	require.NoError(t, err)

	var decoded struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded.Body
}

// encodingJSONBody marshals v with encoding/json.
func encodingJSONBody(t *testing.T, v any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

// assertParity is the assertion the file exists for.
func assertParity(t *testing.T, v any) {
	t.Helper()

	want := encodingJSONBody(t, v)
	got := envelopeBody(t, v)

	assert.Equal(t, want, got,
		"the envelope must serialise this the way encoding/json does")
}

// ----------------------------------------------------------- omitempty parity

type OmitEmptyDto struct {
	NilSlice    []string          `json:"nil_slice,omitempty"`
	EmptySlice  []string          `json:"empty_slice,omitempty"`
	FullSlice   []string          `json:"full_slice,omitempty"`
	NilMap      map[string]string `json:"nil_map,omitempty"`
	EmptyMap    map[string]string `json:"empty_map,omitempty"`
	FullMap     map[string]string `json:"full_map,omitempty"`
	EmptyString string            `json:"empty_string,omitempty"`
	FullString  string            `json:"full_string,omitempty"`
	ZeroInt     int               `json:"zero_int,omitempty"`
	FullInt     int               `json:"full_int,omitempty"`
	FalseBool   bool              `json:"false_bool,omitempty"`
	TrueBool    bool              `json:"true_bool,omitempty"`
	ZeroFloat   float64           `json:"zero_float,omitempty"`
	NilPtr      *string           `json:"nil_ptr,omitempty"`
	SetPtr      *string           `json:"set_ptr,omitempty"`
	ZeroTime    time.Time         `json:"zero_time,omitempty"`
	SetTime     time.Time         `json:"set_time,omitempty"`
	ZeroStruct  Nested            `json:"zero_struct,omitempty"`
	SetStruct   Nested            `json:"set_struct,omitempty"`
	ZeroArray   [0]string         `json:"zero_array,omitempty"`
	FullArray   [2]string         `json:"full_array,omitempty"`
}

type Nested struct {
	Value string `json:"value"`
}

func omitEmptyFixture() OmitEmptyDto {
	set := "set"
	return OmitEmptyDto{
		EmptySlice: []string{},
		FullSlice:  []string{"a"},
		EmptyMap:   map[string]string{},
		FullMap:    map[string]string{"k": "v"},
		FullString: "text",
		FullInt:    7,
		TrueBool:   true,
		SetPtr:     &set,
		SetTime:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		SetStruct:  Nested{Value: "v"},
		FullArray:  [2]string{"a", "b"},
	}
}

// TestOmitEmptyMatchesEncodingJson is the parity assertion over every kind omitempty can
// apply to.
func TestOmitEmptyMatchesEncodingJson(t *testing.T) {
	assertParity(t, omitEmptyFixture())
}

// TestTheSpecificDivergencesAreGone names the four rows that were wrong, so a regression
// reads as a failure about the behaviour rather than a diff of two maps.
func TestTheSpecificDivergencesAreGone(t *testing.T) {
	body := envelopeBody(t, omitEmptyFixture())

	// Were emitted; encoding/json omits them, because a non-nil empty slice or map is
	// zero-length and IsZero() is false for it.
	assert.NotContains(t, body, "empty_slice", "a non-nil empty slice must be omitted")
	assert.NotContains(t, body, "empty_map", "a non-nil empty map must be omitted")

	// Were omitted; encoding/json emits them, because it treats a struct as never empty.
	// This is the dangerous direction: the key silently disappeared.
	assert.Contains(t, body, "zero_time", "a zero time.Time must still be emitted")
	assert.Contains(t, body, "zero_struct", "a zero nested struct must still be emitted")

	// And the rows that always agreed still agree.
	for _, omitted := range []string{
		"nil_slice", "nil_map", "empty_string", "zero_int", "false_bool", "zero_float",
		"nil_ptr", "zero_array",
	} {
		assert.NotContainsf(t, body, omitted, "%s must be omitted", omitted)
	}
	for _, present := range []string{
		"full_slice", "full_map", "full_string", "full_int", "true_bool", "set_ptr",
		"set_time", "set_struct", "full_array",
	} {
		assert.Containsf(t, body, present, "%s must be present", present)
	}
}

// TestIsEmptyValueMatchesEncodingJsonKindByKind pins the predicate itself, independently of
// the marshaller that calls it.
func TestIsEmptyValueMatchesEncodingJsonKindByKind(t *testing.T) {
	set := "s"
	tests := map[string]struct {
		value any
		empty bool
	}{
		"nil slice":       {[]string(nil), true},
		"empty slice":     {[]string{}, true},
		"full slice":      {[]string{"a"}, false},
		"nil map":         {map[string]string(nil), true},
		"empty map":       {map[string]string{}, true},
		"full map":        {map[string]string{"k": "v"}, false},
		"empty string":    {"", true},
		"full string":     {"x", false},
		"zero int":        {0, true},
		"nonzero int":     {1, false},
		"false":           {false, true},
		"true":            {true, false},
		"zero float":      {0.0, true},
		"nonzero float":   {1.5, false},
		"nil pointer":     {(*string)(nil), true},
		"set pointer":     {&set, false},
		"zero struct":     {Nested{}, false},
		"nonzero struct":  {Nested{Value: "v"}, false},
		"zero time":       {time.Time{}, false},
		"empty array":     {[0]string{}, true},
		"non-empty array": {[1]string{"a"}, false},
	}

	for name, tc := range tests {
		assert.Equalf(t, tc.empty, IsEmptyValue(reflect.ValueOf(tc.value)),
			"IsEmptyValue(%s)", name)
	}
}

// ------------------------------------------------ embed/outer collision parity

type CollisionBase struct {
	Label string `json:"label"`
	Only  string `json:"only_in_base"`
}

// EmbedFirstDto declares the embed before the colliding outer field.
type EmbedFirstDto struct {
	CollisionBase
	Label string `json:"label"`
}

// OuterFirstDto declares the colliding outer field before the embed — the order that used
// to invert the result, because the embed's MergeMaps overwrote whatever was already there.
type OuterFirstDto struct {
	Label string `json:"label"`
	CollisionBase
}

// TestTheShallowerFieldWinsWhicheverOrder is the F4 collision headline. encoding/json always
// prefers the outer field; the envelope preferred whichever was declared later, so a struct's
// wire output depended on field order in a way encoding/json never does.
func TestTheShallowerFieldWinsWhicheverOrder(t *testing.T) {
	embedFirst := EmbedFirstDto{CollisionBase{Label: "FROM-EMBED", Only: "base"}, "FROM-OUTER"}
	outerFirst := OuterFirstDto{"FROM-OUTER", CollisionBase{Label: "FROM-EMBED", Only: "base"}}

	t.Run("embed declared first", func(t *testing.T) {
		assertParity(t, embedFirst)
		assert.Equal(t, "FROM-OUTER", envelopeBody(t, embedFirst)["label"])
	})

	t.Run("outer field declared first", func(t *testing.T) {
		assertParity(t, outerFirst)
		assert.Equal(t, "FROM-OUTER", envelopeBody(t, outerFirst)["label"],
			"the outer field must win regardless of declaration order")
	})

	// The same struct, only reordered, must produce the same output.
	assert.Equal(t, envelopeBody(t, embedFirst), envelopeBody(t, outerFirst),
		"reordering the fields must not change the wire output")
}

// TestNonCollidingPromotedFieldsStillInline: the fix must not stop inlining working.
func TestNonCollidingPromotedFieldsStillInline(t *testing.T) {
	dto := OuterFirstDto{"FROM-OUTER", CollisionBase{Label: "FROM-EMBED", Only: "base"}}

	body := envelopeBody(t, dto)
	assert.Equal(t, "base", body["only_in_base"],
		"a promoted field with no collision must still be inlined")
}

// EmbedWithExplicitName carries a json name, so encoding/json makes it a real key at this
// depth rather than inlining it — which means it also *wins* a name collision.
type EmbedWithExplicitName struct {
	CollisionBase `json:"base"`
	Label         string `json:"label"`
}

func TestANamedEmbedIsAKeyNotAnInline(t *testing.T) {
	assertParity(t, EmbedWithExplicitName{
		CollisionBase: CollisionBase{Label: "FROM-EMBED", Only: "base"},
		Label:         "FROM-OUTER",
	})
}

// TestASkippedOuterFieldDoesNotBlockThePromotedOne. `json:"-"` means the outer field is not
// on the wire at all, so it cannot shadow a promoted field of the same name — the shallow-name
// set must exclude it.
func TestASkippedOuterFieldDoesNotBlockThePromotedOne(t *testing.T) {
	type dto struct {
		CollisionBase
		Label string `json:"-"`
	}

	assertParity(t, dto{CollisionBase{Label: "FROM-EMBED", Only: "base"}, "hidden"})

	body := envelopeBody(t, dto{CollisionBase{Label: "FROM-EMBED", Only: "base"}, "hidden"})
	assert.Equal(t, "FROM-EMBED", body["label"],
		"a json:\"-\" outer field is not on the wire, so the promoted one shows through")
}

// TestOmitEmptyOnAPromotedFieldStillApplies: the two fixes interact, so they are checked
// together.
//
// The embedded type has to be *exported*. A lowercase one takes the deliberate
// unexported-embed boundary below instead, which is what caught the first draft of this
// test — the parity assertion is strict enough to fail on a bad fixture, which is the point.
type OmitEmptyBase struct {
	Kept    string    `json:"kept,omitempty"`
	Dropped string    `json:"dropped,omitempty"`
	Time    time.Time `json:"time,omitempty"`
}

type OmitEmptyPromotedDto struct {
	OmitEmptyBase
	Extra string `json:"extra"`
}

func TestOmitEmptyOnAPromotedFieldStillApplies(t *testing.T) {
	value := OmitEmptyPromotedDto{OmitEmptyBase{Kept: "yes"}, "x"}

	assertParity(t, value)

	body := envelopeBody(t, value)
	assert.Contains(t, body, "kept")
	assert.NotContains(t, body, "dropped", "an empty promoted string is still omitted")
	assert.Contains(t, body, "time",
		"but a zero time.Time is emitted, promoted or not")
}

// -------------------------------------- the deliberate, documented differences

// TestLimitedFieldsIsADeliberateDifference. core.LimitedFieldsMarshaller has no
// encoding/json equivalent, so parity does not apply to a DTO that implements it. Pinned so
// the parity work above is not read as a claim about this.
func TestLimitedFieldsIsADeliberateDifference(t *testing.T) {
	body := envelopeBody(t, parityLimitedDto{Shown: "yes", Hidden: "no"})

	assert.Contains(t, body, "shown")
	assert.NotContains(t, body, "hidden",
		"AllowedFields filtering is a framework feature, not a parity gap")

	plain := encodingJSONBody(t, parityLimitedDto{Shown: "yes", Hidden: "no"})
	assert.Contains(t, plain, "hidden", "encoding/json has no such concept")
}

type parityLimitedDto struct {
	Shown  string `json:"shown"`
	Hidden string `json:"hidden"`
}

func (parityLimitedDto) AllowedProtectedFields() []string { return nil }
func (parityLimitedDto) AllowedFields() []string          { return []string{"Shown"} }

// TestAnUnexportedEmbedIsADeliberateDifference. encoding/json promotes the exported fields
// of an embedded unexported type; this marshaller cannot, because it reads values through
// reflect.Value.Interface(), which panics on anything reached through an unexported field.
// Documented in buildBodyElement and in MIGRATION_v2.md.
func TestAnUnexportedEmbedIsADeliberateDifference(t *testing.T) {
	type unexportedBase struct {
		Promoted string `json:"promoted"`
	}
	type dto struct {
		unexportedBase
		Extra string `json:"extra"`
	}

	value := dto{unexportedBase{Promoted: "p"}, "x"}

	assert.Contains(t, encodingJSONBody(t, value), "promoted")
	assert.NotContains(t, envelopeBody(t, value), "promoted",
		"embed an exported type to have its fields inlined")
}
