package middleware

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
)

// AccessCheckerMiddleware used to be a mountable authorization filter that enforced
// nothing. The only code that could supply it an access decision was commented out,
// so its nil branch fired on every request: it logged a warning and called the next
// handler. An app that mounted it over /admin/** believing per-handler access checks
// were being applied had no authorization on those routes at all, and even had the
// decision been available, a denial only produced a 403 when the "namespace" path
// parameter was "api" — every other route fell through to the handler regardless.
//
// It was deleted rather than repaired, because a control whose safe behaviour is
// "refuse everything until an app wires a decision in" is a control the app should
// be writing itself. Declaring the name here keeps the package block occupied: a
// reintroduced AccessCheckerMiddleware collides with this declaration and the
// package stops compiling, which is a louder signal than any runtime assertion
// could give about a middleware whose failure mode is silence.
//
// Both identifiers are held, not just the type. Middlewares in this package are
// reached through a NewXxxMiddleware constructor — NewCorsMiddleware even returns a
// type under an unrelated name (*Cors) — so holding only the struct name would leave
// the reintroduction route the package's own convention points at wide open: a
// constructor called NewAccessCheckerMiddleware handing back some differently-named
// fail-open type compiles perfectly happily against a guard that watches the struct
// name alone.
type AccessCheckerMiddleware struct{}

// NewAccessCheckerMiddleware occupies the constructor name for the reason given
// above. It exists to be collided with and is never meant to be called; it returns
// nothing, so any call site fails to compile too.
func NewAccessCheckerMiddleware() {}

// TestAccessCheckerMiddlewareIsNotAMiddleware pins the guards above to a named test so
// their purpose survives a tidy-up, and asserts the residual name cannot be mounted:
// whatever AccessCheckerMiddleware resolves to in this package, it does not satisfy
// the middleware shape the router accepts.
func TestAccessCheckerMiddlewareIsNotAMiddleware(t *testing.T) {
	// Referencing the constructor placeholder keeps it from reading as dead code that
	// a cleanup would delete, which would silently reopen the name.
	var _ = NewAccessCheckerMiddleware

	var candidate any = AccessCheckerMiddleware{}

	_, mountable := candidate.(interface {
		Handle(next func(core.HttpMessage)) func(core.HttpMessage)
	})

	assert.False(t, mountable, "AccessCheckerMiddleware was removed for enforcing nothing and must not be mountable again")
}
