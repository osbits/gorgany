// Package nomanager exists only to hold tests that must run with no i18n manager
// installed.
//
// i18n.SetManager panics on a second call, so a package that installs one cannot also
// assert the un-installed path: Go compiles a package's tests into a single binary and
// the manager is process-global. validator's own tests install a manager to check
// translation overrides, which leaves this as the only way to cover the branch a CLI app
// actually takes — it validates its command DTOs without ever booting i18n, and
// i18n.GetManager panics when unset.
package nomanager
