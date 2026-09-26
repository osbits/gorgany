package command

import (
	"flag"
	"os"
	"testing"

	"github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/validator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDbCommandResolver returns a Resolver holding the db commands, registered as
// pointers the way CommandProvider registers them. Their own dependencies stay nil:
// resolving a command parses its flags and never executes it.
func newDbCommandResolver(t *testing.T, args ...string) Resolver {
	t.Helper()

	previousArgs, previousHandling := os.Args, flagErrorHandling
	os.Args = append([]string{"cli"}, args...)
	flagErrorHandling = flag.ContinueOnError
	t.Cleanup(func() { os.Args, flagErrorHandling = previousArgs, previousHandling })

	consoleContext := &ConsoleContext{}
	consoleContext.RegisterCommand(&db.DiffCommand{})
	consoleContext.RegisterCommand(&db.MigrateCommand{})
	consoleContext.RegisterCommand(&db.SeedCommand{})

	return Resolver{consoleContext: consoleContext, validator: validator.New()}
}

// TestResolverAcceptsDatasourceOnEveryDbCommand is the regression for
// `cli db:seed --datasource=<name>` exiting 2 with "flag provided but not defined:
// -datasource". The resolver hands os.Args[2:] to a flag set holding only the flags the
// command declares, and SeedCommand and DiffCommand declared none, so the flag was
// rejected before Execute could read it. db:migrate got through only because the flag
// parser stops at its positional `up`.
func TestResolverAcceptsDatasourceOnEveryDbCommand(t *testing.T) {
	cases := [][]string{
		{"db:seed", "--datasource=reports"},
		{"db:seed", "-datasource=reports"},
		{"db:seed", "--datasource", "reports"},
		{"db:diff", "--datasource=reports"},
		{"db:migrate", "up", "--datasource=reports"},
		{"db:migrate", "down", "--datasource=reports", "--steps=2"},
	}

	for _, args := range cases {
		t.Run(args[0]+" "+args[len(args)-1], func(t *testing.T) {
			resolver := newDbCommandResolver(t, args...)

			require.NotPanics(t, func() { resolver.ResolveCommand(args[0]) })
			assert.Equal(t, "reports", db.SelectedDatasource())
		})
	}
}

// TestResolverFillsTheDeclaredDatasourceField checks the declaration is a real flag,
// not merely tolerated: the resolver writes the value into the command.
func TestResolverFillsTheDeclaredDatasourceField(t *testing.T) {
	resolver := newDbCommandResolver(t, "db:seed", "--datasource=reports")
	seed, ok := resolver.ResolveCommand("db:seed").(*db.SeedCommand)
	require.True(t, ok)
	assert.Equal(t, "reports", seed.Datasource)

	resolver = newDbCommandResolver(t, "db:diff")
	diff, ok := resolver.ResolveCommand("db:diff").(*db.DiffCommand)
	require.True(t, ok)
	assert.Equal(t, "default", diff.Datasource, "the flag defaults to the default datasource")
}

// TestResolverStillRejectsAnUndeclaredFlag keeps the flag parser's typo check: declaring
// --datasource must not have made the db commands accept anything.
func TestResolverStillRejectsAnUndeclaredFlag(t *testing.T) {
	resolver := newDbCommandResolver(t, "db:seed", "--datasorce=reports")

	assert.PanicsWithError(t, "flag provided but not defined: -datasorce", func() {
		resolver.ResolveCommand("db:seed")
	})
}
