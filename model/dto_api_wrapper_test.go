package model

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The envelope marshaller does not go through encoding/json — it reflects field by
// field — so these tests compare its output against encoding/json's, which is the
// behaviour every app author already expects from a json tag.

// marshalBody renders payload through the envelope and returns the decoded body.
func marshalBody(t *testing.T, payload any) map[string]any {
	t.Helper()

	envelope := &ApiReturnObject{Body: payload, HttpStatus: core.SuccessHttpStatus}

	raw, err := json.Marshal(envelope)
	require.NoError(t, err)

	var decoded struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded.Body
}

// referenceJSON renders payload through encoding/json, the behaviour to match.
func referenceJSON(t *testing.T, payload any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// ------------------------------------------------------------ json:"-" (security)

type userWithSecret struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	InternalNote string `json:"-"`
}

// TestJSONDashIsHonoured is the T3.5 security fix. parseJSONTag treated
// `json:"-"` identically to an absent tag and returned the Go field name for both,
// so a DTO field marked json:"-" to keep a password hash off the wire was
// serialised anyway — under its Go name.
func TestJSONDashIsHonoured(t *testing.T) {
	payload := userWithSecret{
		ID:           "u1",
		Email:        "ann@example.com",
		PasswordHash: "$2a$10$do-not-leak-me",
		InternalNote: "flagged for review",
	}

	body := marshalBody(t, payload)

	assert.Equal(t, "u1", body["id"])
	assert.Equal(t, "ann@example.com", body["email"])

	// The whole point: neither the tag-suppressed field nor its Go-named twin.
	assert.NotContains(t, body, "PasswordHash")
	assert.NotContains(t, body, "password_hash")
	assert.NotContains(t, body, "InternalNote")

	raw, err := json.Marshal(&ApiReturnObject{Body: payload, HttpStatus: core.SuccessHttpStatus})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "do-not-leak-me",
		"the secret must not appear anywhere in the response")

	assert.Equal(t, referenceJSON(t, payload), body, "must match encoding/json")
}

// TestJSONDashCommaIsALiteralName covers encoding/json's one subtlety: `json:"-"`
// means skip, but `json:"-,"` means "use the literal name -".
func TestJSONDashCommaIsALiteralName(t *testing.T) {
	type dashNamed struct {
		Weird string `json:"-,"`
	}

	payload := dashNamed{Weird: "value"}
	body := marshalBody(t, payload)

	assert.Equal(t, "value", body["-"])
	assert.Equal(t, referenceJSON(t, payload), body)
}

// --------------------------------------------------------------------- omitempty

type withOmitEmpty struct {
	Name     string            `json:"name"`
	Nickname string            `json:"nickname,omitempty"`
	Age      int               `json:"age,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	Active   bool              `json:"active,omitempty"`
	Ptr      *string           `json:"ptr,omitempty"`
}

// TestOmitEmptyIsHonoured — omitempty was ignored, so an empty field shipped as
// null / "" / 0 and clients could not tell "absent" from "empty".
func TestOmitEmptyIsHonoured(t *testing.T) {
	payload := withOmitEmpty{Name: "ann"}

	body := marshalBody(t, payload)

	assert.Equal(t, "ann", body["name"])
	for _, omitted := range []string{"nickname", "age", "tags", "meta", "active", "ptr"} {
		assert.NotContainsf(t, body, omitted, "%s is zero and tagged omitempty", omitted)
	}
}

func TestOmitEmptyKeepsNonZeroValues(t *testing.T) {
	value := "p"
	payload := withOmitEmpty{
		Name:     "ann",
		Nickname: "a",
		Age:      30,
		Tags:     []string{"x"},
		Meta:     map[string]string{"k": "v"},
		Active:   true,
		Ptr:      &value,
	}

	body := marshalBody(t, payload)

	assert.Equal(t, "a", body["nickname"])
	assert.Equal(t, float64(30), body["age"])
	assert.Equal(t, true, body["active"])
	assert.NotNil(t, body["tags"])
	assert.NotNil(t, body["meta"])
	assert.NotNil(t, body["ptr"])
}

// TestFieldWithoutOmitEmptyIsAlwaysPresent guards against over-applying the rule.
func TestFieldWithoutOmitEmptyIsAlwaysPresent(t *testing.T) {
	payload := withOmitEmpty{}

	body := marshalBody(t, payload)

	require.Contains(t, body, "name")
	assert.Equal(t, "", body["name"])
}

// -------------------------------------------------------------- embedded structs

type OwnerCardDto struct {
	OwnerID   string `json:"owner_id"`
	OwnerName string `json:"owner_name"`
}

type cardDto struct {
	OwnerCardDto
	CardID string `json:"card_id"`
}

// TestAnonymousEmbeddedStructIsInlined — an embedded struct was written under its
// Go *type* name, so a response carried {"OwnerCardDto": {...}} where
// encoding/json produces the fields inline. Any app with an embedded DTO had a
// response shape that did not match its own json tags.
func TestAnonymousEmbeddedStructIsInlined(t *testing.T) {
	payload := cardDto{
		OwnerCardDto: OwnerCardDto{OwnerID: "o1", OwnerName: "Ann"},
		CardID:       "c1",
	}

	body := marshalBody(t, payload)

	assert.NotContains(t, body, "OwnerCardDto", "the Go type name must never be a key")
	assert.Equal(t, "o1", body["owner_id"])
	assert.Equal(t, "Ann", body["owner_name"])
	assert.Equal(t, "c1", body["card_id"])

	assert.Equal(t, referenceJSON(t, payload), body, "must match encoding/json")
}

type cardWithPointerEmbed struct {
	*OwnerCardDto
	CardID string `json:"card_id"`
}

func TestAnonymousEmbeddedPointerIsInlined(t *testing.T) {
	payload := cardWithPointerEmbed{
		OwnerCardDto: &OwnerCardDto{OwnerID: "o1", OwnerName: "Ann"},
		CardID:       "c1",
	}

	body := marshalBody(t, payload)

	assert.Equal(t, "o1", body["owner_id"])
	assert.Equal(t, "c1", body["card_id"])
	assert.NotContains(t, body, "OwnerCardDto")
	assert.Equal(t, referenceJSON(t, payload), body)
}

func TestNilAnonymousEmbeddedPointerContributesNothing(t *testing.T) {
	payload := cardWithPointerEmbed{CardID: "c1"}

	body := marshalBody(t, payload)

	assert.Equal(t, "c1", body["card_id"])
	assert.NotContains(t, body, "owner_id")
	assert.NotContains(t, body, "OwnerCardDto")
	assert.Equal(t, referenceJSON(t, payload), body)
}

type namedEmbed struct {
	OwnerCardDto `json:"owner"`
	CardID       string `json:"card_id"`
}

// TestNamedEmbeddedStructIsNotInlined: encoding/json does not inline an embedded
// field whose tag names it, and neither do we.
func TestNamedEmbeddedStructIsNotInlined(t *testing.T) {
	payload := namedEmbed{
		OwnerCardDto: OwnerCardDto{OwnerID: "o1", OwnerName: "Ann"},
		CardID:       "c1",
	}

	body := marshalBody(t, payload)

	require.Contains(t, body, "owner")
	owner, ok := body["owner"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "o1", owner["owner_id"])
	assert.NotContains(t, body, "owner_id")

	assert.Equal(t, referenceJSON(t, payload), body)
}

type embeddedWithSecret struct {
	Secret string `json:"-"`
	Public string `json:"public"`
}

type outerOverEmbeddedSecret struct {
	embeddedWithSecret
	ID string `json:"id"`
}

// ExportedWithSecret is the supported shape: an embedded exported type.
type ExportedWithSecret struct {
	Secret string `json:"-"`
	Public string `json:"public"`
}

type OuterOverEmbeddedSecret struct {
	ExportedWithSecret
	ID string `json:"id"`
}

// TestEmbeddedUnexportedTypeIsSkipped documents a boundary rather than a fix.
//
// encoding/json promotes the exported fields of an embedded struct even when the
// struct's own type is unexported. This marshaller cannot: it reads values through
// reflect.Value.Interface(), which panics on anything reached through an unexported
// field. The pre-v2 code skipped these too, so nothing regressed — but the
// behaviour differs from encoding/json and is worth pinning so it cannot change by
// accident. Embed an exported type to have its fields inlined.
func TestEmbeddedUnexportedTypeIsSkipped(t *testing.T) {
	payload := outerOverEmbeddedSecret{
		embeddedWithSecret: embeddedWithSecret{Secret: "leak-me-not", Public: "fine"},
		ID:                 "x",
	}

	body := marshalBody(t, payload)

	assert.Equal(t, "x", body["id"])
	assert.NotContains(t, body, "public", "an embedded unexported type is not promoted")

	// The security guarantee still holds either way: the suppressed value is absent.
	assert.NotContains(t, body, "Secret")
	assert.NotContains(t, body, "secret")

	raw, err := json.Marshal(&ApiReturnObject{Body: payload, HttpStatus: core.SuccessHttpStatus})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "leak-me-not")

	// This is the case where the two marshallers legitimately differ.
	assert.NotEqual(t, referenceJSON(t, payload), body)
}

// TestJSONDashInsideAnInlinedEmbeddedStructIsHonoured combines both fixes for the
// supported shape: an embedded *exported* type carrying a suppressed field.
func TestJSONDashInsideAnInlinedEmbeddedStructIsHonoured(t *testing.T) {
	payload := OuterOverEmbeddedSecret{
		ExportedWithSecret: ExportedWithSecret{Secret: "leak-me-not", Public: "fine"},
		ID:                 "x",
	}

	body := marshalBody(t, payload)

	assert.Equal(t, "fine", body["public"], "the embedded exported type is inlined")
	assert.Equal(t, "x", body["id"])
	assert.NotContains(t, body, "Secret")
	assert.NotContains(t, body, "secret")

	raw, err := json.Marshal(&ApiReturnObject{Body: payload, HttpStatus: core.SuccessHttpStatus})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "leak-me-not")

	assert.Equal(t, referenceJSON(t, payload), body, "must match encoding/json")
}

// TestEmbeddedInterfaceIsNotInlined: an embedded interface is a normal field, as
// encoding/json treats it.
func TestEmbeddedInterfaceIsNotInlined(t *testing.T) {
	type withEmbeddedInterface struct {
		error        // an embedded interface, nil here
		ID    string `json:"id"`
	}

	payload := withEmbeddedInterface{ID: "x"}

	require.NotPanics(t, func() {
		body := marshalBody(t, payload)
		assert.Equal(t, "x", body["id"])
	})
}

// -------------------------------------------------- unchanged behaviour guards

type limitedDto struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Secret string `json:"secret"`
}

func (limitedDto) AllowedFields() []string          { return []string{"ID", "Email"} }
func (limitedDto) AllowedProtectedFields() []string { return nil }

// TestLimitedFieldsMarshallerStillFilters keeps the RBAC-style field filtering
// working alongside the tag handling.
func TestLimitedFieldsMarshallerStillFilters(t *testing.T) {
	var _ core.LimitedFieldsMarshaller = limitedDto{}

	body := marshalBody(t, limitedDto{ID: "1", Email: "a@b.c", Secret: "s"})

	assert.Equal(t, "1", body["id"])
	assert.Equal(t, "a@b.c", body["email"])
	assert.NotContains(t, body, "secret", "a field outside AllowedFields must be dropped")
}

func TestUnexportedFieldsAreStillSkipped(t *testing.T) {
	type withUnexported struct {
		ID     string `json:"id"`
		hidden string
	}

	body := marshalBody(t, withUnexported{ID: "1", hidden: "x"})

	assert.Equal(t, "1", body["id"])
	assert.NotContains(t, body, "hidden")
}

func TestUntaggedFieldsUseTheirGoName(t *testing.T) {
	type untagged struct {
		Name string
		Age  int
	}

	payload := untagged{Name: "ann", Age: 30}
	body := marshalBody(t, payload)

	assert.Equal(t, "ann", body["Name"])
	assert.Equal(t, float64(30), body["Age"])
	assert.Equal(t, referenceJSON(t, payload), body)
}

func TestSliceAndMapBodiesStillWork(t *testing.T) {
	envelope := &ApiReturnObject{
		Body:       []userWithSecret{{ID: "1", PasswordHash: "nope"}, {ID: "2"}},
		HttpStatus: core.SuccessHttpStatus,
	}

	raw, err := json.Marshal(envelope)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "nope", "json:\"-\" must hold inside a slice too")

	var decoded struct {
		Body []map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Body, 2)
	assert.Equal(t, "1", decoded.Body[0]["id"])
	assert.NotContains(t, decoded.Body[0], "PasswordHash")
}

func TestMapBodyHonoursTagsOnItsValues(t *testing.T) {
	envelope := &ApiReturnObject{
		Body:       map[string]userWithSecret{"a": {ID: "1", PasswordHash: "nope"}},
		HttpStatus: core.SuccessHttpStatus,
	}

	raw, err := json.Marshal(envelope)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "nope")
}

// ------------------------------------------------------------- parseJSONTag unit

func TestParseJSONTag(t *testing.T) {
	type sample struct {
		Absent      string
		Empty       string `json:""`
		Named       string `json:"named"`
		Skipped     string `json:"-"`
		LiteralDash string `json:"-,"`
		OmitOnly    string `json:",omitempty"`
		NamedOmit   string `json:"named_omit,omitempty"`
		Stringed    string `json:"stringed,string,omitempty"`
	}

	rt := reflect.TypeOf(sample{})
	byName := func(name string) jsonTag {
		field, ok := rt.FieldByName(name)
		require.True(t, ok)
		return parseJSONTag(field)
	}

	assert.Equal(t, jsonTag{Name: "Absent"}, byName("Absent"))
	assert.Equal(t, jsonTag{Name: "Empty"}, byName("Empty"))
	assert.Equal(t, jsonTag{Name: "named", HasName: true}, byName("Named"))
	assert.Equal(t, jsonTag{Name: "Skipped", Skip: true}, byName("Skipped"))
	assert.Equal(t, jsonTag{Name: "-", HasName: true}, byName("LiteralDash"))
	assert.Equal(t, jsonTag{Name: "OmitOnly", OmitEmpty: true}, byName("OmitOnly"))
	assert.Equal(t, jsonTag{Name: "named_omit", HasName: true, OmitEmpty: true}, byName("NamedOmit"))
	assert.Equal(t, jsonTag{Name: "stringed", HasName: true, OmitEmpty: true}, byName("Stringed"))
}

// Reaching the `nestedElement.(map[string]any)` assertion in buildBodyElement takes a
// specific shape, and finding it is the point: the embedded field must marshal itself
// (so buildBodyElement hands back a json.RawMessage) while the *outer* DTO must not
// (or buildBodyElement would return early at its own json.Marshaler check).
//
// Method promotion normally forbids that combination — embed one self-marshalling type
// and the outer struct promotes MarshalJSON. Embed *two* at the same depth and the
// selector is ambiguous, so neither is promoted: the outer DTO is not a json.Marshaler,
// but each field still is. A DTO embedding two self-marshalling helper types is an
// ordinary thing to write, and it used to panic on the response path.

type SelfMarshallingA struct {
	Name string
}

func (SelfMarshallingA) AllowedProtectedFields() []string { return nil }

func (SelfMarshallingA) AllowedFields() []string { return []string{"*"} }

func (s SelfMarshallingA) MarshalJSON() ([]byte, error) {
	return []byte(`{"name":"` + s.Name + `"}`), nil
}

type SelfMarshallingB struct {
	Other string
}

func (SelfMarshallingB) AllowedProtectedFields() []string { return nil }

func (SelfMarshallingB) AllowedFields() []string { return []string{"*"} }

func (s SelfMarshallingB) MarshalJSON() ([]byte, error) {
	return []byte(`{"other":"` + s.Other + `"}`), nil
}

type DtoWithTwoSelfMarshallingEmbeds struct {
	SelfMarshallingA
	SelfMarshallingB
	Extra string `json:"extra"`
}

func (DtoWithTwoSelfMarshallingEmbeds) AllowedProtectedFields() []string { return nil }

func (DtoWithTwoSelfMarshallingEmbeds) AllowedFields() []string { return []string{"*"} }

// TestASelfMarshallingEmbeddedDtoIsAnErrorNotAPanic covers the last B1 site.
func TestASelfMarshallingEmbeddedDtoIsAnErrorNotAPanic(t *testing.T) {
	// Guard the premise: if either of these ever stops holding, the test is no longer
	// exercising the branch it claims to.
	var dto any = DtoWithTwoSelfMarshallingEmbeds{}
	_, outerMarshals := dto.(json.Marshaler)
	require.False(t, outerMarshals, "the ambiguous selector must leave the outer DTO a plain struct")
	var field any = SelfMarshallingA{}
	_, fieldMarshals := field.(json.Marshaler)
	require.True(t, fieldMarshals)

	obj := &ApiReturnObject{
		HttpStatus: core.SuccessHttpStatus,
		Body: DtoWithTwoSelfMarshallingEmbeds{
			SelfMarshallingA: SelfMarshallingA{Name: "ann"},
			SelfMarshallingB: SelfMarshallingB{Other: "b"},
			Extra:            "x",
		},
	}

	var err error
	require.NotPanics(t, func() { _, err = json.Marshal(obj) })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SelfMarshalling")
	assert.Contains(t, err.Error(), "marshals itself")
}

// EmbeddedLimited is the ordinary case the branch exists for: embedded, limited, and
// not self-marshalling. Its allowed fields are inlined.
//
// The type has to be exported for its fields to be promoted at all — an embedded field
// of unexported type has an unexported field name, which this marshaller skips (see the
// note in buildBodyElement).
type EmbeddedLimited struct {
	Name   string `json:"name"`
	Secret string `json:"-"`
}

func (EmbeddedLimited) AllowedProtectedFields() []string { return nil }

func (EmbeddedLimited) AllowedFields() []string { return []string{"Name"} }

type DtoWithLimitedEmbed struct {
	EmbeddedLimited
	Extra string `json:"extra"`
}

func (DtoWithLimitedEmbed) AllowedProtectedFields() []string { return nil }

func (DtoWithLimitedEmbed) AllowedFields() []string { return []string{"*"} }

// TestALimitedEmbeddedDtoStillInlines keeps the working path honest alongside the fix.
func TestALimitedEmbeddedDtoStillInlines(t *testing.T) {
	obj := &ApiReturnObject{
		HttpStatus: core.SuccessHttpStatus,
		Body:       DtoWithLimitedEmbed{EmbeddedLimited{Name: "ann", Secret: "s"}, "x"},
	}

	raw, err := json.Marshal(obj)
	require.NoError(t, err)

	var decoded struct {
		Body map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))

	assert.Equal(t, "ann", decoded.Body["name"], "the embedded field is inlined")
	assert.Equal(t, "x", decoded.Body["extra"])
	assert.NotContains(t, decoded.Body, "Secret")
	assert.NotContains(t, decoded.Body, "EmbeddedLimited")
}
