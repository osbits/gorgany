package validator

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	goValidator "github.com/go-playground/validator/v10"
	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------- helpers

func newValidator(t *testing.T) *Validator {
	t.Helper()
	return New().(*Validator)
}

// errorsFor validates s and returns the failures keyed by wire field name.
func errorsFor(t *testing.T, v *Validator, s any) map[string]error2.ValidationError {
	t.Helper()

	err := v.ValidateStruct(s)
	require.Error(t, err, "the fixture is supposed to fail validation")

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)

	out := make(map[string]error2.ValidationError, len(*validationErrors))
	for _, e := range *validationErrors {
		out[e.Field] = e
	}
	return out
}

// ------------------------------------------------------------------ fixtures

type AddressDto struct {
	PostalCode string `json:"postal_code" validate:"required,len=4"`
}

type CreateUserDto struct {
	Email       string     `json:"email" validate:"required,email"`
	MobilePhone string     `json:"mobile_phone" validate:"required"`
	Age         int        `json:"age" validate:"gte=18"`
	Role        string     `json:"role" validate:"oneof=admin user"`
	Address     AddressDto `json:"address" validate:"required"`
	Untagged    string     `validate:"required"`
}

// SearchDto binds from a query string, so it carries scheme rather than json tags.
type SearchDto struct {
	Term string `scheme:"q" validate:"required"`
	Page int    `scheme:"page" validate:"gte=1"`
}

// ------------------------------------------------------- field naming (B2 core)

// TestErrorsCarryTheWireFieldName is the B2 headline. Field used to be the Go struct
// name, so a client that sent `mobile_phone` got told `MobilePhone` failed and had
// nothing to match it against.
func TestErrorsCarryTheWireFieldName(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{})

	assert.Contains(t, found, "email")
	assert.Contains(t, found, "mobile_phone")
	assert.Contains(t, found, "age")
	assert.NotContains(t, found, "MobilePhone", "the Go name must not appear")
	assert.NotContains(t, found, "Email")
}

// TestASchemeTaggedDtoIsNamedByItsSchemeTag: query-string and multipart DTOs bind
// through `scheme`, not `json`, so both tags have to be consulted.
func TestASchemeTaggedDtoIsNamedByItsSchemeTag(t *testing.T) {
	found := errorsFor(t, newValidator(t), SearchDto{})

	assert.Contains(t, found, "q")
	assert.Contains(t, found, "page")
	assert.NotContains(t, found, "Term")
}

// TestAnUntaggedFieldFallsBackToItsGoName: a field with neither tag has no wire name,
// and the Go name is the only honest answer.
func TestAnUntaggedFieldFallsBackToItsGoName(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{})
	assert.Contains(t, found, "Untagged")
}

// TestNestedFieldsCarryAFollowablePath: a client that sent
// {"address": {"postal_code": ...}} needs the path, not just the leaf name.
func TestNestedFieldsCarryAFollowablePath(t *testing.T) {
	v := newValidator(t)
	err := v.ValidateStruct(CreateUserDto{Address: AddressDto{PostalCode: "toolong"}})
	require.Error(t, err)

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)

	var postal *error2.ValidationError
	for i := range *validationErrors {
		if (*validationErrors)[i].Field == "postal_code" {
			postal = &(*validationErrors)[i]
		}
	}
	require.NotNil(t, postal, "the nested failure must be reported")

	assert.Equal(t, "address.postal_code", postal.Path,
		"the path is the wire path, with the DTO type name dropped")
	assert.Equal(t, "len", postal.Rule)
	assert.Equal(t, "4", postal.Param)
}

// TestATopLevelFieldsPathEqualsItsName keeps Path meaningful for the common case.
func TestATopLevelFieldsPathEqualsItsName(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{})
	assert.Equal(t, "email", found["email"].Path)
}

// --------------------------------------------------------------- messages (B2)

// TestMessagesAreHumanReadable. Err used to be go-playground's raw sentence:
// "Key: 'CreateUserDto.Email' Error:Field validation for 'Email' failed on the 'email'
// tag" — undisplayable and unparseable.
func TestMessagesAreHumanReadable(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{Email: "not-an-email", Age: 12})

	assert.Equal(t, "email must be a valid email address", found["email"].Err)
	assert.Equal(t, "mobile_phone is required", found["mobile_phone"].Err)
	assert.Equal(t, "age must be greater than or equal to 18", found["age"].Err)
	assert.Equal(t, "role must be one of: admin user", found["role"].Err)

	for field, e := range found {
		assert.NotContains(t, e.Err, "Key:", "%s still carries the raw go-playground text", field)
		assert.NotContains(t, e.Err, "Error:Field validation", "%s still carries the raw text", field)
	}
}

// TestTheRuleAndParamAreReportedSeparately: a client keying its own copy off the rule
// should not have to parse the message to find it.
func TestTheRuleAndParamAreReportedSeparately(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{Age: 12})

	assert.Equal(t, "required", found["email"].Rule)
	assert.Empty(t, found["email"].Param, "a rule with no parameter reports none")
	assert.Equal(t, "gte", found["age"].Rule)
	assert.Equal(t, "18", found["age"].Param)
}

type customRuleDto struct {
	Token string `json:"token" validate:"neverpasses"`
}

// TestAnAppRegisteredRuleGetsTheCatchAll, which names the rule so an app that has not
// yet added a message still gets something actionable rather than opaque.
func TestAnAppRegisteredRuleGetsTheCatchAll(t *testing.T) {
	v := newValidator(t)
	require.NoError(t, v.RegisterValidation("neverpasses",
		func(goValidator.FieldLevel) bool { return false }))

	found := errorsFor(t, v, customRuleDto{Token: "anything"})

	require.Contains(t, found, "token")
	assert.Equal(t, "token failed the neverpasses rule", found["token"].Err)
	assert.Equal(t, "neverpasses", found["token"].Rule)
}

// TestEveryFrameworkRuleHasAMessage is the guard that keeps a newly registered custom
// rule from shipping with only the catch-all.
func TestEveryFrameworkRuleHasAMessage(t *testing.T) {
	messages := DefaultMessages()

	for _, rule := range []string{
		"mime", "maxSize", "unique",
		"lsCompletelyRequired", "mapStringStringCompletelyRequired",
	} {
		assert.Containsf(t, messages, rule,
			"the framework registers %q in New(), so it needs a message", rule)
	}

	assert.Contains(t, messages, "default", "the catch-all must exist")
}

// TestDefaultMessagesIsACopy: handing out the live map would let one app's edit leak
// into every other validator in the process.
func TestDefaultMessagesIsACopy(t *testing.T) {
	first := DefaultMessages()
	first["required"] = "tampered"

	assert.Equal(t, "{:field} is required", DefaultMessages()["required"])
}

// TestEveryDefaultMessageNamesItsField, so no message is a bare "is required".
func TestEveryDefaultMessageNamesItsField(t *testing.T) {
	for rule, msg := range DefaultMessages() {
		assert.Containsf(t, msg, "{:field}", "the message for %q does not name the field", rule)
	}
}

// The un-booted-i18n path is covered in validator/nomanager, which is a separate
// package because i18n.SetManager panics on a second call and this package installs a
// manager in messages_i18n_test.go.

// TestTheErrorSerialisesForAClient pins the JSON a client actually receives.
func TestTheErrorSerialisesForAClient(t *testing.T) {
	found := errorsFor(t, newValidator(t), CreateUserDto{Age: 12})

	raw, err := json.Marshal(found["age"])
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	assert.Equal(t, "age", decoded["field"])
	assert.Equal(t, "age must be greater than or equal to 18", decoded["err"])
	assert.Equal(t, "gte", decoded["rule"])
	assert.Equal(t, "18", decoded["param"])
	assert.Equal(t, "age", decoded["path"])
}

// TestOmittedFieldsStayOutOfTheJson: rule/param/path are omitempty, so an error
// constructed by hand serialises to the pre-v2 two-key shape.
func TestOmittedFieldsStayOutOfTheJson(t *testing.T) {
	raw, err := json.Marshal(error2.ValidationError{Field: "email", Err: "bad"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"field":"email","err":"bad"}`, string(raw))
}

// ------------------------------------------------ getOverriddenFields (B2 tail)

type ShadowBase struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"name" validate:"required"`
}

type ShadowingDto struct {
	ShadowBase
	// Email shadows ShadowBase.Email. In Go the promoted one is unreachable, so
	// validating it would report a failure the client cannot possibly fix.
	Email string `json:"email" validate:"required"`
}

// TestAShadowedEmbeddedFieldIsNotValidatedTwice. This is what the exclusion list is
// for, and it only worked by accident before: the namespaces were built from wire names
// while StructExcept matches Go names, so every exclusion was a silent no-op.
func TestAShadowedEmbeddedFieldIsNotValidatedTwice(t *testing.T) {
	v := newValidator(t)

	excluded, err := v.overriddenFields(ShadowingDto{})
	require.NoError(t, err)
	assert.Equal(t, []string{"ShadowBase.Email"}, excluded,
		"the namespace must use Go names, which is what StructExcept matches")

	found := errorsFor(t, v, ShadowingDto{})
	assert.Contains(t, found, "email")
	assert.Contains(t, found, "name")

	// And exactly one failure for email, not two.
	err = v.ValidateStruct(ShadowingDto{})
	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)

	emails := 0
	for _, e := range *validationErrors {
		if e.Field == "email" {
			emails++
		}
	}
	assert.Equal(t, 1, emails, "the shadowed copy must be excluded")
}

// TestTheShadowingFieldsOwnRulesApply: excluding the promoted copy must not exclude the
// outer field too.
func TestTheShadowingFieldsOwnRulesApply(t *testing.T) {
	v := newValidator(t)

	// ShadowingDto.Email is only `required`, not `email`, so a non-address passes.
	err := v.ValidateStruct(ShadowingDto{Email: "not-an-address", ShadowBase: ShadowBase{Name: "n"}})
	assert.NoError(t, err, "the outer field's own rules are the ones that apply")

	// But it is still required.
	err = v.ValidateStruct(ShadowingDto{ShadowBase: ShadowBase{Name: "n"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "email is required")
}

type EmbedA struct {
	Email string `json:"email"`
	Name  string `json:"name" validate:"required"`
}

type EmbedB struct {
	Phone string `json:"phone"`
	Name  string `json:"other_name" validate:"required"`
}

type TwoEmbedsDto struct {
	EmbedA
	EmbedB
	Name string `json:"name" validate:"required"`
}

// TestTwoEmbeddedStructsGetIndependentNamespaces is the nondeterminism bug. parentKey
// was reassigned inside the loop over embedded fields and accumulated across
// iterations, so a struct embedding A and B produced ["A.Name", "A.B.Name"] where the
// second should have been "B.Name" — and Go randomises map iteration, so which struct
// got the corrupt namespace varied per run.
func TestTwoEmbeddedStructsGetIndependentNamespaces(t *testing.T) {
	v := newValidator(t)

	// Run repeatedly: the old defect surfaced only for some iteration orders.
	for i := 0; i < 50; i++ {
		excluded, err := v.overriddenFields(TwoEmbedsDto{})
		require.NoError(t, err)

		assert.ElementsMatch(t, []string{"EmbedA.Name", "EmbedB.Name"}, excluded,
			"each embedded struct's namespace is derived from its own name")
		for _, ns := range excluded {
			assert.NotContains(t, ns, "EmbedA.EmbedB",
				"the namespace must not accumulate across embedded fields")
		}
	}
}

type SelfEmbedding struct {
	*SelfEmbedding
	Name string `json:"name" validate:"required"`
}

// TestASelfEmbeddingStructTerminates. Without a cycle guard the walk recurses until the
// stack runs out; this is a legal Go type (a linked-list node, for instance).
func TestASelfEmbeddingStructTerminates(t *testing.T) {
	v := newValidator(t)

	var excluded []string
	var err error
	require.NotPanics(t, func() { excluded, err = v.overriddenFields(SelfEmbedding{}) })
	require.NoError(t, err)
	assert.Empty(t, excluded)
}

type PtrEmbedA struct {
	Name string `json:"name" validate:"required"`
}

type PtrEmbedB struct {
	Name string `json:"name" validate:"required"`
}

type PointerEmbedsDto struct {
	*PtrEmbedA
	*PtrEmbedB
	Name string `json:"name" validate:"required"`
}

// TestEmbeddedPointersAreKeyedByFieldNameNotTypeName. The old walk keyed embedded
// fields by reflect.Type.Name(), which is "" for a pointer type — so two embedded
// pointers collapsed onto one map entry and all but one were dropped.
func TestEmbeddedPointersAreKeyedByFieldNameNotTypeName(t *testing.T) {
	// Prove the premise rather than asserting it in prose.
	field, ok := reflect.TypeOf(PointerEmbedsDto{}).FieldByName("PtrEmbedA")
	require.True(t, ok)
	assert.Empty(t, field.Type.Name(), "a pointer type is unnamed")
	assert.Equal(t, "PtrEmbedA", field.Name, "the field name is not")

	v := newValidator(t)
	excluded, err := v.overriddenFields(PointerEmbedsDto{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PtrEmbedA.Name", "PtrEmbedB.Name"}, excluded)
}

// TestAValueStructDoesNotPanic. The old walk called field.Addr(), which panics with
// "reflect.Value.Addr of unaddressable value" for a struct passed by value —
// ValidateStruct(SomeDto{}) went down outright.
func TestAValueStructDoesNotPanic(t *testing.T) {
	v := newValidator(t)

	var err error
	require.NotPanics(t, func() { err = v.ValidateStruct(TwoEmbedsDto{}) })
	require.Error(t, err, "it must validate, not merely survive")
	assert.Contains(t, err.Error(), "is required")
}

// TestANilEmbeddedPointerDoesNotPanic. The old walk recursed through the pointer's
// value and died with "reflect: call of reflect.Value.Type on zero Value".
func TestANilEmbeddedPointerDoesNotPanic(t *testing.T) {
	v := newValidator(t)

	var err error
	require.NotPanics(t, func() { err = v.ValidateStruct(&PointerEmbedsDto{}) })
	assert.Error(t, err)
}

// collidingDto builds the DTO at runtime rather than declaring it, because `go vet`'s
// structtag check rejects a literal duplicate tag — the very shape this test needs. The
// type it builds is indistinguishable from a declared one as far as reflect is
// concerned, which is all the walk sees.
func collidingDto() any {
	t := reflect.StructOf([]reflect.StructField{
		{Name: "Primary", Type: reflect.TypeOf(""), Tag: `json:"email" validate:"required"`},
		{Name: "Secondary", Type: reflect.TypeOf(""), Tag: `json:"email" validate:"required"`},
	})
	return reflect.New(t).Elem().Interface()
}

// TestTwoFieldsOnOneWireNameFailLoudly is the brief's "either make that safe or fail
// loudly on the collision". The body parser can bind only one of them, so the other
// silently stays zero and validation reports a name matching neither.
func TestTwoFieldsOnOneWireNameFailLoudly(t *testing.T) {
	v := newValidator(t)

	err := v.ValidateStruct(collidingDto())
	require.Error(t, err)

	var validationErrors *error2.ValidationErrors
	assert.False(t, errors.As(err, &validationErrors),
		"this is a DTO defect, not a client's input being wrong")

	assert.Contains(t, err.Error(), `wire name "email"`)
	assert.Contains(t, err.Error(), "Primary")
	assert.Contains(t, err.Error(), "Secondary")
}

// TestShadowingIsNotMistakenForACollision: a shadowing field legitimately reuses the
// promoted field's wire name, and the collision check is scoped per struct so it does
// not fire on that.
func TestShadowingIsNotMistakenForACollision(t *testing.T) {
	v := newValidator(t)

	_, err := v.overriddenFields(ShadowingDto{})
	assert.NoError(t, err)
}

// TestNonStructInputIsLeftToStructExcept, which reports it far more precisely.
func TestNonStructInputIsLeftToStructExcept(t *testing.T) {
	v := newValidator(t)

	for _, input := range []any{nil, "text", 42, []string{"a"}, map[string]string{}} {
		excluded, err := v.overriddenFields(input)
		assert.NoError(t, err)
		assert.Empty(t, excluded)
	}
}

// TestUnexportedFieldsAreSkipped: validate rules cannot apply to them and reflect
// cannot read them.
func TestUnexportedFieldsAreSkipped(t *testing.T) {
	type withUnexported struct {
		Name   string `json:"name" validate:"required"`
		hidden string
	}

	v := newValidator(t)
	var err error
	require.NotPanics(t, func() { err = v.ValidateStruct(withUnexported{}) })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hidden")
}

// ---------------------------------------------------------------- localisation

// TestValidateStructForLocaleIsReachableThroughTheOptionalInterface: the HTTP input
// resolver type-asserts for it, so it has to be satisfied by what New() returns.
func TestValidateStructForLocaleIsReachableThroughTheOptionalInterface(t *testing.T) {
	var v core.IValidator = New()

	localized, ok := v.(core.ILocalizedValidator)
	require.True(t, ok, "New() must return something the input resolver can localise through")

	err := localized.ValidateStructForLocale(CreateUserDto{}, "xx-unconfigured")
	require.Error(t, err)
	// An unconfigured locale falls back rather than blanking, so the English defaults
	// come through.
	assert.Contains(t, err.Error(), "is required")
}
