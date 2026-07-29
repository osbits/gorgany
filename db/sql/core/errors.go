package core

import "fmt"

// UnsupportedError reports a query construct the target engine cannot express.
//
// A dialect returns this instead of emitting SQL the server will reject: the
// failure then surfaces at the call site that built the query, naming the
// construct, rather than as an opaque syntax error from the driver.
type UnsupportedError struct {
	// Dialect is the dialect name, e.g. "mysql".
	Dialect string
	// Construct is the offending construct, e.g. "DISTINCT ON".
	Construct string
	// Hint, when set, names the portable alternative.
	Hint string
}

func (e *UnsupportedError) Error() string {
	msg := fmt.Sprintf("%s does not support %s", e.Dialect, e.Construct)
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

// Unsupported builds an UnsupportedError.
func Unsupported(dialect, construct, hint string) error {
	return &UnsupportedError{Dialect: dialect, Construct: construct, Hint: hint}
}

// IsUnsupported reports whether err is an UnsupportedError.
func IsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*UnsupportedError)
	return ok
}
