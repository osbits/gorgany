package err

import (
	"errors"
	"fmt"
	"github.com/osbits/gorgany/v2/log"
	"runtime"
	"strings"
)

func GetStacktrace() string {
	buf := make([]byte, 1<<16)
	runtime.Stack(buf, false)
	stack := fmt.Sprintf("%s", buf)
	splitStack := strings.Split(stack, "\n")
	return strings.Join(splitStack[0:len(splitStack)-1], "\n")
}

func PrintError(err any) {
	log.Log("").Errorf("%s\n%s", err, GetStacktrace())
}

func HandleError(err any) {
	if err == nil {
		return
	}
	_, file, line, _ := runtime.Caller(1)
	//pc, file, line, _ := runtime.Caller(1)
	//funcName := runtime.FuncForPC(pc).Name()
	log.Log("").Errorf("\u001B[0;31mError in \u001B[0m%s:%d: %v\n", file, line, err)
}

func HandleErrorWithStacktrace(err any) {
	if err == nil {
		return
	}
	PrintError(err)
}

// Validation
type ValidationErrors []ValidationError

func (thiz ValidationErrors) Error() string {
	errs := make([]string, 0)
	for _, err := range thiz {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, "\n")
}

func (thiz *ValidationErrors) AddValidationError(validationError ValidationError) {
	*thiz = append(*thiz, validationError)
}

func (thiz *ValidationErrors) Unwrap() error {
	return errors.New("validation errors")
}

// ValidationError is one failed rule on one field, as a client receives it.
//
// Field and Err are unchanged in name and position; what they contain changed in v2.0.
// Field used to be the Go struct field name (`MobilePhone` for something the client
// sent as `mobile_phone`), and Err used to be go-playground's raw
// `Key: 'Dto.Email' Error:Field validation for 'Email' failed on the 'email' tag` — a
// string no UI could display and no client could map to a form field. See docs/VALIDATION.md.
type ValidationError struct {
	// Field is the wire name of the field: its json or scheme tag, falling back to the
	// Go field name for a field that has neither.
	Field string `json:"field"`
	// Err is the human-readable message, localised through the app's translations when
	// it supplies them.
	Err string `json:"err"`
	// Rule is the validation tag that failed — `required`, `email`, `min`. A client
	// keying its own copy off the rule rather than the message is why this is here.
	Rule string `json:"rule,omitempty"`
	// Param is the rule's parameter, e.g. "3" for `min=3`. Empty for a rule that takes
	// none.
	Param string `json:"param,omitempty"`
	// Path is the dotted wire path to the field for a nested DTO, e.g.
	// `address.postal_code`. Equal to Field for a top-level field.
	Path string `json:"path,omitempty"`
}

func (thiz ValidationError) String() string {
	return fmt.Sprintf("Field: %s, Error: %v", thiz.Field, thiz.Err)
}

func (thiz ValidationError) Error() string {
	return thiz.String()
}

func (thiz ValidationError) Unwrap() error {
	return fmt.Errorf("%w: %s", &ValidationErrors{}, thiz.Err)
}

// InputParamParseError
func NewInputParamParseError(value string, kind string) *InputParamParseError {
	return &InputParamParseError{
		Value: value,
		Type:  kind,
	}
}

type InputParamParseError struct {
	Value string
	Type  string
}

func (thiz InputParamParseError) Error() string {
	return fmt.Sprintf("Unable to convert `%s` to %s", thiz.Value, thiz.Type)
}

// InputBodyParseError
func NewInputBodyParseError(body string, kind string, err error) *InputBodyParseError {
	return &InputBodyParseError{
		Body:     body,
		Type:     kind,
		RawError: err,
	}
}

// InputBodyParseError is a request body the framework could not parse.
type InputBodyParseError struct {
	// Body is the raw request body, kept so a caller that genuinely needs it can reach
	// it — and deliberately NOT included in Error().
	//
	// A body that failed to parse is exactly the kind that carries a half-typed
	// password: a truncated POST to a login route puts a cleartext credential here.
	// Error() strings end up in logs by default in most codebases, so anything in
	// Error() is effectively logged. Reading this field is an explicit choice; do not
	// make it in a log line or a response.
	Body string
	// Type is the content type the parser was working in.
	Type string
	// RawError is why parsing failed. Safe to log and to show a client: the framework's
	// own parsers put only offsets, sizes, limits and type names here.
	//
	// One nuance, since overclaiming would be worse than stating it: a wrapped
	// *json.SyntaxError renders its own message, which for some inputs names the single
	// offending character ("invalid character 'q' after object key"). One byte, and it
	// is what makes the error diagnosable.
	RawError error
}

// Error describes the failure without reproducing the body.
//
// It used to be "Unable to convert body from %s. Error: %v\nBody: %s". The framework's
// own handler opens with PrintError(err), so every malformed body was written verbatim to
// the log at Error level — and from there to `docker logs` and any aggregator. That was
// new exposure: before InputBodyParseError was constructed at all, the parse path
// produced an empty ValidationErrors and logged nothing.
func (thiz InputBodyParseError) Error() string {
	return fmt.Sprintf("unable to parse body as %s: %v", thiz.Type, thiz.RawError)
}

// JwtAuthError
func NewJwtAuthError() *JwtAuthError {
	return &JwtAuthError{}
}

type JwtAuthError struct{}

func (thiz JwtAuthError) Error() string {
	return "Unauthenticated. JWT is invalid or expired"
}
