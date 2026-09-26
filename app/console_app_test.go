package app

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
)

const consoleChildEnv = "GORGANY_CONSOLE_TEST_CHILD"

// TestConsoleAppChildWithoutACommand is the child's entry point, not a test of its own. It
// runs ConsoleApp with no arguments after the binary name, which calls os.Exit.
func TestConsoleAppChildWithoutACommand(t *testing.T) {
	if os.Getenv(consoleChildEnv) != "1" {
		t.Skip("runs only as the child of TestConsoleAppWithoutACommandExitsNonZero")
	}

	os.Args = os.Args[:1]
	NewConsoleApp(bootstrapperFunc(func(core.IContainer) {
		t.Error("the application booted with no command to run")
	})).Run()
}

// TestConsoleAppWithoutACommandExitsNonZero: a deploy step that lost its arguments used to
// print a message and exit 0, so it passed. It has to fail, and before booting, which
// opens the database pools.
func TestConsoleAppWithoutACommandExitsNonZero(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestConsoleAppChildWithoutACommand$")
	cmd.Dir = t.TempDir() // no .env or config: booting would fail differently
	cmd.Env = append(os.Environ(), consoleChildEnv+"=1")

	output, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "the child must exit non-zero: %v\n%s", err, output)
	assert.Equal(t, 2, exitErr.ExitCode(), "%s", output)
	assert.Contains(t, string(output), "Command name must be presented")
	assert.NotContains(t, string(output), "Gorgany framework is starting")
}
