package testsupport

import "os"

// unset removes a variable. It exists so a test can distinguish "absent" from "set to
// empty", which is the distinction envOr is built around.
func unset(name string) error {
	return os.Unsetenv(name)
}
