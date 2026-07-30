package nomanager_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/i18n"
	"github.com/osbits/gorgany/v2/validator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliCommandDto struct {
	Name  string `json:"name" validate:"required"`
	Count int    `json:"count" validate:"gte=1"`
}

// TestValidationWithoutABootedI18nDoesNotPanic. The message catalog looks messages up
// through i18n, and i18n.GetManager panics when no manager is installed. A CLI app
// validates its command DTOs (command/resolver.go) without ever booting i18n, so
// panicking there would turn a bad flag into a crash.
func TestValidationWithoutABootedI18nDoesNotPanic(t *testing.T) {
	require.False(t, i18n.HasManager(),
		"this package exists to assert the un-booted path; nothing may install a manager")

	v := validator.New()

	var err error
	require.NotPanics(t, func() { err = v.ValidateStruct(cliCommandDto{}) })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required",
		"the framework's English defaults apply when there is nothing to translate with")
	assert.Contains(t, err.Error(), "count must be greater than or equal to 1")
}

// TestTheDefaultLocaleIsSafeToAskForUnbooted: ValidateStruct resolves the default locale
// from viper, which is also unconfigured here.
func TestTheDefaultLocaleIsSafeToAskForUnbooted(t *testing.T) {
	require.NotPanics(t, func() { _ = i18n.DefaultLocale() })
}
