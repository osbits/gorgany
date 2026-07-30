package validator

// This file is separate from validator_test.go because it installs an i18n manager,
// which i18n.SetManager permits exactly once per process. validator_test.go asserts the
// un-booted path, so the two cannot share a package-level manager — Go runs the tests in
// one binary.
//
// The manager is installed lazily by the one test that needs it and never removed, so
// every test that must see an un-booted i18n has to live in the other file.

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogConfig struct {
	values map[string]string
}

func (c catalogConfig) GetString(key string) string { return c.values[key] }

var _ core.Ii18nConfig = catalogConfig{}

type dtoUnderI18n struct {
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age" validate:"gte=18"`
	Role  string `json:"role" validate:"oneof=admin user"`
}

// TestTranslationsOverrideTheDefaults is the B2 requirement that an app can supply
// another language "without reimplementing the validator" — which is what every
// consumer had to do while the message was go-playground's raw English sentence.
func TestTranslationsOverrideTheDefaults(t *testing.T) {
	i18n.SetManager(i18n.Manager{
		FallbackTag: "en",
		Configs: map[string]core.Ii18nConfig{
			"de": catalogConfig{values: map[string]string{
				"validation.required": "{:field} ist erforderlich",
				"validation.email":    "{:field} muss eine gültige E-Mail-Adresse sein",
				// gte is deliberately absent, to prove a partial catalog still works.
			}},
			"uk": catalogConfig{values: map[string]string{
				// Only the catch-all, to prove it backs every unlisted rule.
				"validation.default": "поле {:field} не пройшло правило {:rule}",
			}},
			"en": catalogConfig{},
		},
	})
	require.True(t, i18n.HasManager())

	v := New().(*Validator)

	t.Run("a translated rule uses the translation", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Age: 12}, "de")
		assert.Equal(t, "email ist erforderlich", found["email"].Err)
	})

	t.Run("an untranslated rule falls back to the framework default", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Age: 12}, "de")
		assert.Equal(t, "age must be greater than or equal to 18", found["age"].Err,
			"a partial catalog must not leave the message blank")
	})

	t.Run("the placeholder is substituted", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Email: "nope", Age: 12}, "de")
		assert.Equal(t, "email muss eine gültige E-Mail-Adresse sein", found["email"].Err)
		assert.NotContains(t, found["email"].Err, "{:field}")
	})

	t.Run("a catch-all-only catalog covers every rule", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Age: 12}, "uk")
		assert.Equal(t, "поле email не пройшло правило required", found["email"].Err)
		assert.Equal(t, "поле age не пройшло правило gte", found["age"].Err)
		assert.Equal(t, "поле role не пройшло правило oneof", found["role"].Err)
	})

	t.Run("an unconfigured locale falls back rather than blanking", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Age: 12}, "ja")
		// GetConfig falls back to "en", whose catalog is empty, so the framework
		// defaults apply. Blank messages would be worse than English ones.
		assert.Equal(t, "email is required", found["email"].Err)
	})

	t.Run("the field name is still the wire name", func(t *testing.T) {
		found := localisedErrors(t, v, dtoUnderI18n{Age: 12}, "de")
		assert.Contains(t, found, "email")
		assert.NotContains(t, found, "Email")
	})
}

func localisedErrors(t *testing.T, v *Validator, s any, locale string) map[string]error2.ValidationError {
	t.Helper()

	err := v.ValidateStructForLocale(s, locale)
	require.Error(t, err)

	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)

	out := make(map[string]error2.ValidationError, len(*validationErrors))
	for _, e := range *validationErrors {
		out[e.Field] = e
	}
	return out
}
