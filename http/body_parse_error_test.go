package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	error2 "github.com/osbits/gorgany/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B3: err.NewInputBodyParseError had zero call sites while http/error.go and
// e2e/fixture-app both registered handlers for it, so app authors reasonably believed it
// was the hook for malformed bodies. It was not — every parse failure arrived as
// ValidationErrors, and a syntax error arrived as an *empty* one, which
// processValidationErrors turned into a 301 redirect to the Referer.

func parseBody(t *testing.T, body string) error {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, "/widgets", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}
	return parser.Parse(&JSONTestStruct{})
}

// TestASyntaxErrorIsABodyParseError is the headline: a truncated body is a 400-class
// parse failure, not a 422-class validation failure.
func TestASyntaxErrorIsABodyParseError(t *testing.T) {
	err := parseBody(t, `{"name": "widget"`)

	var parseError *error2.InputBodyParseError
	require.ErrorAs(t, err, &parseError, "a malformed body must be an InputBodyParseError")

	assert.Equal(t, "application/json", parseError.Type)
	assert.Equal(t, `{"name": "widget"`, parseError.Body, "the body is kept for the log")
	require.Error(t, parseError.RawError)
	assert.Contains(t, parseError.RawError.Error(), "invalid JSON syntax at byte")
}

// TestATopLevelNonObjectIsABodyParseError. Unmarshalling into map[string]interface{}
// accepts any JSON object, so this is the reachable UnmarshalTypeError case — which the
// old code tried to catch with errors.Is and could not.
func TestATopLevelNonObjectIsABodyParseError(t *testing.T) {
	bodies := map[string]string{
		"array":   `[{"name":"a"}]`,
		"string":  `"just a string"`,
		"number":  `42`,
		"boolean": `true`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			err := parseBody(t, body)

			var parseError *error2.InputBodyParseError
			require.ErrorAs(t, err, &parseError)
			assert.Contains(t, parseError.RawError.Error(), "must be a JSON object")
		})
	}
}

// TestTheOldErrorsIsChecksCouldNeverMatch proves why the switch was dead, rather than
// asserting it in a comment. errors.Is compares with == for a type implementing no Is
// method, and both operands were freshly allocated pointers.
func TestTheOldErrorsIsChecksCouldNeverMatch(t *testing.T) {
	syntaxErr := json.Unmarshal([]byte(`{`), &map[string]any{})
	require.Error(t, syntaxErr)

	assert.False(t, errors.Is(syntaxErr, &json.SyntaxError{}),
		"errors.Is against a fresh pointer is always false — this is what shipped")
	var target *json.SyntaxError
	assert.True(t, errors.As(syntaxErr, &target), "errors.As is the call that works")
}

// TestAnOversizedBodyIsABodyParseError. It used to be a ValidationErrors naming a "body"
// field, which no form has.
func TestAnOversizedBodyIsABodyParseError(t *testing.T) {
	oversized := `{"name":"` + strings.Repeat("x", maxJSONSize) + `"}`
	err := parseBody(t, oversized)

	var parseError *error2.InputBodyParseError
	require.ErrorAs(t, err, &parseError)
	assert.Contains(t, parseError.RawError.Error(), "over the")
	assert.Contains(t, parseError.RawError.Error(), "byte limit")
}

// TestAnOverNestedBodyIsABodyParseError covers checkJSONDepth's refusal.
func TestAnOverNestedBodyIsABodyParseError(t *testing.T) {
	deep := strings.Repeat(`{"a":`, maxJSONDepth+1) + `1` + strings.Repeat(`}`, maxJSONDepth+1)
	err := parseBody(t, deep)

	var parseError *error2.InputBodyParseError
	require.ErrorAs(t, err, &parseError)
	assert.Contains(t, parseError.RawError.Error(), "nesting exceeds")
}

// TestAWellFormedBodyStillParses guards the happy path against the new error branches.
func TestAWellFormedBodyStillParses(t *testing.T) {
	assert.NoError(t, parseBody(t, `{"name":"widget","age":3}`))
}

// TestAnEmptyBodyIsNotAnError: a POST with no body leaves the DTO at its zero value and
// lets validation decide, which is existing behaviour worth pinning.
func TestAnEmptyBodyIsNotAnError(t *testing.T) {
	assert.NoError(t, parseBody(t, ""))
}

// TestALiteralNullBodyIsAcceptedAsNoFields. `null` unmarshals into a map without error,
// leaving it nil — so unlike every other top-level non-object it is not a parse failure.
// Pinned because it is the one exception to the rule above.
func TestALiteralNullBodyIsAcceptedAsNoFields(t *testing.T) {
	assert.NoError(t, parseBody(t, `null`))
}

// TestAFieldLevelProblemIsStillAValidationError is the other side of the split: a body
// that parses but holds a value a field cannot take is 422, not 400.
func TestAFieldLevelProblemIsStillAValidationError(t *testing.T) {
	err := parseBody(t, `{"age":"not-a-number"}`)

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors,
		"a value the field rejects is a validation failure, not a parse failure")
	assert.NotEmpty(t, *validationErrors, "and it must carry at least one entry")

	var parseError *error2.InputBodyParseError
	assert.False(t, errors.As(err, &parseError))
}

// TestEveryParseFailureCarriesAReason: the handler renders RawError, so an empty one
// would produce a 400 with nothing in it — which is what the old Text("", 400) did.
func TestEveryParseFailureCarriesAReason(t *testing.T) {
	for _, body := range []string{
		`{"name": "widget"`,
		`[1,2]`,
		`<xml/>`,
		strings.Repeat(`{"a":`, maxJSONDepth+1) + `1` + strings.Repeat(`}`, maxJSONDepth+1),
	} {
		err := parseBody(t, body)

		var parseError *error2.InputBodyParseError
		require.ErrorAsf(t, err, &parseError, "body %q", body)
		require.Errorf(t, parseError.RawError, "body %q has no reason attached", body)
		assert.NotEmptyf(t, parseError.RawError.Error(), "body %q", body)
	}
}
