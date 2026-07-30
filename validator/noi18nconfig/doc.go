// Package noi18nconfig asserts that H5's warning stays silent for an app that never
// configured i18n at all.
//
// It is a package of its own for the same reason validator/nomanager is: the assertion is
// about process-global state that cannot be undone. The warning fires at most once per
// process (sync.Once, so a client cannot flood the log by posting empty bodies in a loop),
// so any test that provokes it consumes the only observation available. Asserting "nothing
// was logged" is only meaningful in a binary where nothing else could have logged it — and
// in validator/nomanager something does, deliberately.
//
// Nothing here may install an i18n manager or set any `i18n` config key.
package noi18nconfig
