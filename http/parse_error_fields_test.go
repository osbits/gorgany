package http

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G2: checkAndAddIfValidationError's guard was errors.Is(err, &error2.ValidationError{}),
// which is always false — no Is method, freshly allocated target — and tested the singular
// type where the parsers produce the plural one. So every query-string and multipart parse
// failure was flattened to field "GeneralError" with the real field name left in prose, and
// B2's promise that a client can map an error to the field it sent did not hold on two of
// the three input paths.

// ------------------------------------------------------------------- fixtures

type fieldNameQueryDto struct {
	Limit  int    `scheme:"limit"`
	Offset int    `scheme:"offset"`
	Search string `scheme:"search"`
}

func (fieldNameQueryDto) ContentType() core.ContentType { return core.Query }

type fieldNameUploadDto struct {
	Count int `scheme:"count"`
}

func (fieldNameUploadDto) ContentType() core.ContentType { return core.MultipartFormData }

// parseQuery runs the query parser and returns the validation errors it produced.
func parseQuery(t *testing.T, rawQuery string) error2.ValidationErrors {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)
	parser := &QueryParser{message: &queryMockHttpMessage{req: req}}

	err := parser.Parse(&fieldNameQueryDto{})
	require.Error(t, err, "the fixture is supposed to fail parsing")

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)
	return *validationErrors
}

// fieldsOf indexes errors by the field they name.
func fieldsOf(errs error2.ValidationErrors) map[string]error2.ValidationError {
	out := make(map[string]error2.ValidationError, len(errs))
	for _, e := range errs {
		out[e.Field] = e
	}
	return out
}

// ---------------------------------------------------------------------- tests

// TestAQueryParseErrorNamesTheField is the headline. This used to be "GeneralError".
func TestAQueryParseErrorNamesTheField(t *testing.T) {
	found := fieldsOf(parseQuery(t, "limit=bad"))

	require.Contains(t, found, "limit",
		"the wire field name must be reported, not GeneralError")
	assert.NotContains(t, found, core.GeneralError)

	// And the message is the parser's own, not a wrapped chain with the field name
	// serialised into it.
	assert.Contains(t, found["limit"].Err, "bad")
	assert.NotContains(t, found["limit"].Err, "failed to process field",
		"the wrapper's prose must not reach the client")
	assert.NotContains(t, found["limit"].Err, "Field: limit, Error:",
		"nor a nested ValidationError rendered by String()")
}

// TestEachBadQueryFieldIsNamedSeparately: a client fixing a form needs one entry per field,
// not one lump.
func TestEachBadQueryFieldIsNamedSeparately(t *testing.T) {
	// initStruct returns on the first failure, so drive them one at a time — the point is
	// that whichever field fails is the one named.
	for _, field := range []string{"limit", "offset"} {
		t.Run(field, func(t *testing.T) {
			found := fieldsOf(parseQuery(t, field+"=notanumber"))
			assert.Contains(t, found, field)
			assert.NotContains(t, found, core.GeneralError)
		})
	}
}

// TestAMultipartParseErrorNamesTheField covers the other live call site.
func TestAMultipartParseErrorNamesTheField(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("count", "not-a-number"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	parser := &MultipartParser{message: &mockHttpMessage{req: req}}
	err := parser.Parse(&fieldNameUploadDto{})
	require.Error(t, err)

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)

	found := fieldsOf(*validationErrors)
	assert.Contains(t, found, "count")
	assert.NotContains(t, found, core.GeneralError)
}

// ------------------------------------------------- the helper, directly

// TestTheHelperUnwrapsAPluralValidationErrors is the shape the parsers actually produce,
// reached through initStruct's fmt.Errorf(... %w ...) wrapper.
func TestTheHelperUnwrapsAPluralValidationErrors(t *testing.T) {
	inner := &error2.ValidationErrors{
		{Field: "email", Err: "email is required", Rule: "required", Path: "email"},
		{Field: "age", Err: "age must be numeric", Rule: "numeric", Param: "", Path: "age"},
	}
	wrapped := fmt.Errorf("failed to process field email: %w", inner)

	collected := make(error2.ValidationErrors, 0)
	checkAndAddIfValidationError(wrapped, &collected)

	require.Len(t, collected, 2, "every entry must survive, not be flattened to one")

	found := fieldsOf(collected)
	assert.Equal(t, "email is required", found["email"].Err)
	assert.Equal(t, "required", found["email"].Rule, "Rule must survive the unwrap")
	assert.Equal(t, "email", found["email"].Path, "and Path")
	assert.Contains(t, found, "age")
	assert.NotContains(t, found, core.GeneralError)
}

// TestTheHelperAcceptsASingularValidationError, the other shape it claimed to handle — and
// the one whose branch would have panicked, asserting the value type after comparing the
// pointer type.
func TestTheHelperAcceptsASingularValidationError(t *testing.T) {
	single := &error2.ValidationError{Field: "token", Err: "token is malformed", Rule: "len"}

	collected := make(error2.ValidationErrors, 0)
	require.NotPanics(t, func() { checkAndAddIfValidationError(single, &collected) })

	require.Len(t, collected, 1)
	assert.Equal(t, "token", collected[0].Field)
	assert.Equal(t, "len", collected[0].Rule)
}

// TestTheOldGuardCouldNeverMatch proves why the branch was dead rather than asserting it in
// prose. Same demonstration as the JSON parser's in B3.
func TestTheOldGuardCouldNeverMatch(t *testing.T) {
	err := error(&error2.ValidationError{Field: "f", Err: "e"})

	assert.False(t, errors.Is(err, &error2.ValidationError{}),
		"errors.Is against a freshly allocated target is always false — this is what shipped")

	var target *error2.ValidationError
	assert.True(t, errors.As(err, &target), "errors.As is the call that works")
}

// TestANonValidationErrorStillFallsBackToGeneralError. The fallback is correct when there is
// genuinely no field to name — a reflection failure, a bad destination — so it must survive.
func TestANonValidationErrorStillFallsBackToGeneralError(t *testing.T) {
	collected := make(error2.ValidationErrors, 0)
	checkAndAddIfValidationError(errors.New("destination must be a pointer"), &collected)

	require.Len(t, collected, 1)
	assert.Equal(t, core.GeneralError, collected[0].Field)
	assert.Equal(t, "destination must be a pointer", collected[0].Err)
}

// TestAnEmptyNestedValidationErrorsDoesNotVanish: a non-nil but empty *ValidationErrors must
// not swallow the error, or the caller returns a failure with nothing in it — which is
// exactly how B3's malformed bodies became a 301 to the Referer.
func TestAnEmptyNestedValidationErrorsDoesNotVanish(t *testing.T) {
	empty := &error2.ValidationErrors{}
	wrapped := fmt.Errorf("something went wrong: %w", empty)

	collected := make(error2.ValidationErrors, 0)
	checkAndAddIfValidationError(wrapped, &collected)

	require.NotEmpty(t, collected, "an empty nested slice must still produce an entry")
	assert.Equal(t, core.GeneralError, collected[0].Field)
}
