package validator

import (
	"fmt"
	"strings"

	goValidator "github.com/go-playground/validator/v10"
	"github.com/osbits/gorgany/v2/i18n"
)

// MessageCodePrefix is prepended to a rule name to form the i18n code a message is
// looked up under: the `email` rule reads `validation.email`.
//
// An app localises validation by adding these keys to its translation files. Nothing
// else is required — in particular, replacing core.IValidator wholesale is not, which
// is what every consumer had to do while the only message available was
// go-playground's raw `Key: 'Dto.Email' Error:Field validation for 'Email' failed on
// the 'email' tag`.
const MessageCodePrefix = "validation."

// FallbackMessageCode is looked up for a rule with no entry of its own, in the app's
// translations and then in defaultMessages. It is what a custom rule registered by an
// app gets until the app adds a message for it.
const FallbackMessageCode = MessageCodePrefix + "default"

// defaultMessages is the built-in English catalog, keyed by rule name.
//
// Placeholders use i18n's `{:key}` syntax and are substituted through i18n.Interpolate,
// the same function that renders a translation, so a framework default and an app
// override behave identically:
//
//	{:field}  the field's wire name — its json/scheme tag, not the Go name
//	{:param}  the rule's parameter, e.g. the 3 in `min=3`
//	{:value}  the rejected value, rendered with %v
//
// Only rules the framework itself registers, plus the go-playground built-ins a web DTO
// actually reaches for, are listed. Anything else falls through to the default message,
// which names the rule so the error is still actionable.
var defaultMessages = map[string]string{
	// presence
	"required":         "{:field} is required",
	"required_if":      "{:field} is required",
	"required_unless":  "{:field} is required",
	"required_with":    "{:field} is required",
	"required_without": "{:field} is required",
	"excluded_with":    "{:field} must not be present",

	// length and range. min/max/len apply to a string's length, a number's value and a
	// collection's item count depending on the field, so the wording stays neutral.
	"min":   "{:field} must be at least {:param}",
	"max":   "{:field} must be at most {:param}",
	"len":   "{:field} must be exactly {:param}",
	"gt":    "{:field} must be greater than {:param}",
	"gte":   "{:field} must be greater than or equal to {:param}",
	"lt":    "{:field} must be less than {:param}",
	"lte":   "{:field} must be less than or equal to {:param}",
	"ne":    "{:field} must not be {:param}",
	"eq":    "{:field} must be {:param}",
	"oneof": "{:field} must be one of: {:param}",

	// format
	"email":      "{:field} must be a valid email address",
	"url":        "{:field} must be a valid URL",
	"uri":        "{:field} must be a valid URI",
	"uuid":       "{:field} must be a valid UUID",
	"uuid4":      "{:field} must be a valid UUID",
	"numeric":    "{:field} must be numeric",
	"number":     "{:field} must be a number",
	"alpha":      "{:field} must contain only letters",
	"alphanum":   "{:field} must contain only letters and digits",
	"ascii":      "{:field} must contain only ASCII characters",
	"boolean":    "{:field} must be true or false",
	"datetime":   "{:field} must be a date in the format {:param}",
	"e164":       "{:field} must be a valid phone number in E.164 format",
	"hexcolor":   "{:field} must be a valid hex colour",
	"ip":         "{:field} must be a valid IP address",
	"json":       "{:field} must be valid JSON",
	"lowercase":  "{:field} must be lowercase",
	"uppercase":  "{:field} must be uppercase",
	"contains":   "{:field} must contain {:param}",
	"startswith": "{:field} must start with {:param}",
	"endswith":   "{:field} must end with {:param}",
	"eqfield":    "{:field} must match {:param}",
	"nefield":    "{:field} must not match {:param}",
	"gtfield":    "{:field} must be greater than {:param}",
	"ltfield":    "{:field} must be less than {:param}",

	// the framework's own rules, registered in New()
	"mime":                              "{:field} must be a file of type {:param}",
	"maxSize":                           "{:field} must not be larger than {:param}",
	"unique":                            "{:field} is already taken",
	"lsCompletelyRequired":              "{:field} is required in every language",
	"mapStringStringCompletelyRequired": "{:field} is required for every key",

	// the catch-all, which names the rule so an unlisted or app-registered one is still
	// actionable rather than opaque
	"default": "{:field} failed the {:rule} rule",
}

// DefaultMessages returns a copy of the built-in English catalog, keyed by rule name.
//
// It is exported so an app can see what it is overriding — and so a test can assert
// every rule the framework registers has a message, which is what keeps a newly added
// custom rule from shipping with only the catch-all.
func DefaultMessages() map[string]string {
	out := make(map[string]string, len(defaultMessages))
	for rule, msg := range defaultMessages {
		out[rule] = msg
	}
	return out
}

// message renders the human-readable message for one field error.
//
// Resolution order, first non-empty wins:
//
//  1. the app's translation for `validation.<rule>` in the requested locale
//  2. the app's translation for `validation.default`
//  3. the framework's English message for `<rule>`
//  4. the framework's English catch-all
//
// The app's translations come first so overriding one rule does not mean restating the
// rest, and the framework default backs every rule so a partial translation file still
// produces a readable message.
func message(e goValidator.FieldError, fieldName, locale string) string {
	opts := map[string]any{
		"field": fieldName,
		"rule":  e.Tag(),
		"param": e.Param(),
		"value": renderValue(e),
	}

	// A CLI app validates its command DTOs without ever booting i18n, so the lookup is
	// skipped rather than panicking inside GetManager.
	if i18n.HasManager() {
		for _, code := range []string{MessageCodePrefix + e.Tag(), FallbackMessageCode} {
			if translated := i18n.Translation(code, opts, locale); translated != "" {
				return translated
			}
		}
	}

	template, ok := defaultMessages[e.Tag()]
	if !ok {
		template = defaultMessages["default"]
	}
	return i18n.Interpolate(template, opts)
}

// renderValue renders the rejected value for the `{:value}` placeholder.
//
// No default message uses it, deliberately: echoing a rejected value back is how a
// password or a token ends up in a log or an error response. It is available for an app
// that wants it on a specific rule, and it is truncated so a rejected 2 MB body does not
// become a 2 MB error string.
func renderValue(e goValidator.FieldError) string {
	const limit = 64

	rendered := fmt.Sprintf("%v", e.Value())
	if len(rendered) > limit {
		return rendered[:limit] + "…"
	}
	return rendered
}

// wireNamespace turns go-playground's namespace into the dotted path a client can
// follow, dropping the leading struct-type segment.
//
// go-playground reports `CreateUserDto.address.postal_code`; a client sent
// `{"address": {"postal_code": ...}}` and wants `address.postal_code`. The segments are
// already wire names because New() registers a tag-name function.
func wireNamespace(e goValidator.FieldError) string {
	namespace := e.Namespace()
	if _, rest, found := strings.Cut(namespace, "."); found {
		return rest
	}
	return namespace
}
