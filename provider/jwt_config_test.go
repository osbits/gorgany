package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadConfig puts real YAML into viper, because two of the cases under test — a YAML null
// and a literal empty string — only exist once a parser has been through the file.
func loadConfig(t *testing.T, yaml string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(yaml)))
}

// bootPanic runs the provider the way Bootstrapper does and reports what, if anything, it
// panicked with.
func bootPanic(t *testing.T) (message string, panicked bool) {
	t.Helper()

	container := service.NewContainer()
	AppProvider{}.Register(container)

	defer func() {
		if recovered := recover(); recovered != nil {
			message = fmt.Sprint(recovered)
			panicked = true
		}
	}()

	AppProvider{}.Boot(container)
	return "", false
}

const strongSecret = "PyD8yhvAyBFC0Qs4Q9k1TfKp7cJmVn2xLr6WdZbGtHs"

// TestBootFailsForAnUnusableJwtSecret.
//
// Every one of these config shapes used to boot clean, with no error and no warning, and
// left the framework signing and accepting tokens with a key an attacker can reproduce.
// An app that declares the auth.jwt section means to use JWT, so this is a boot failure
// rather than a warning nobody reads.
func TestBootFailsForAnUnusableJwtSecret(t *testing.T) {
	tests := map[string]string{
		"the key is absent from a jwt section": `
auth:
  session:
    lifeTime: 3600
  jwt:
    lifeTime: 3600
`,
		"a literal empty string": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: ""
    lifeTime: 3600
`,
		"a yaml null": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret:
    lifeTime: 3600
`,
		"whitespace only": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: "   "
    lifeTime: 3600
`,
		"an unresolved placeholder": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: "${JWT_SECRET}"
    lifeTime: 3600
`,
		"a weak secret": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: s3cret
    lifeTime: 3600
`,
		"a secret padded to length": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    lifeTime: 3600
`,
		// A passphrase, which is the shape an operator reaches for when told to invent a
		// secret. It is varied enough that the repetition rule does not touch it, so the
		// documented length floor is the only thing that stops this boot — and at 31 bytes
		// it is the closest miss there is.
		"a varied secret one byte under the floor": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: correct-horse-battery-staple-01
    lifeTime: 3600
`,
		// Long enough to clear both floors, so the boot stops only because the value is
		// still a placeholder. An app that substitutes `${VAR}` itself, or one that runs
		// ResolveEnvPlaceholders with KeepUnresolvedLiterals, arrives here.
		"a placeholder longer than the floor": `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: "${APPLICATION_JWT_SIGNING_SECRET}"
    lifeTime: 3600
`,
	}

	for name, yaml := range tests {
		t.Run(name, func(t *testing.T) {
			loadConfig(t, yaml)

			message, panicked := bootPanic(t)

			require.True(t, panicked, "the boot must stop, not carry on with an unusable key")
			assert.Contains(t, message, "auth.jwt.secret",
				"the operator must be told which key is wrong")
		})
	}
}

// TestAnExplicitlyEmptyEnvironmentVariableStopsTheBoot is the realistic ops mistake:
// JWT_SECRET= in a .env, a compose file or a CI secret store. os.LookupEnv reports it as
// present, so placeholder resolution substitutes it verbatim — deliberately, because an
// empty string is a legitimate value for other keys — and the result is an empty signing
// key.
func TestAnExplicitlyEmptyEnvironmentVariableStopsTheBoot(t *testing.T) {
	t.Setenv("JWT_SECRET", "")

	loadConfig(t, `
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: ${JWT_SECRET}
    lifeTime: 3600
`)
	viper.Set("auth.jwt.secret", "") // what ResolveEnvPlaceholders leaves behind

	message, panicked := bootPanic(t)

	require.True(t, panicked)
	assert.Contains(t, message, "auth.jwt.secret")
}

// TestBootSucceedsForAStrongJwtSecret — the guard must not stop a correctly configured app.
func TestBootSucceedsForAStrongJwtSecret(t *testing.T) {
	loadConfig(t, fmt.Sprintf(`
auth:
  session:
    lifeTime: 3600
  jwt:
    secret: %s
    lifeTime: 3600
`, strongSecret))

	_, panicked := bootPanic(t)
	assert.False(t, panicked)
}

// TestBootSucceedsForAnAppThatDoesNotUseJwt.
//
// "Is JWT in use here" is answered by the config: an app whose configuration declares no
// auth.jwt key has not asked for JWT and must not be made to invent a secret to boot. Such
// an app is still safe, because every JWT entry point refuses an unusable key at the point
// of use — including IsRequestMadeWithStrategy, which is the one an unconfigured app can
// otherwise be dragged into by a request carrying a bearer token.
func TestBootSucceedsForAnAppThatDoesNotUseJwt(t *testing.T) {
	loadConfig(t, `
auth:
  session:
    storage: memory
    lifeTime: 3600
`)

	_, panicked := bootPanic(t)
	assert.False(t, panicked, "a session-only app must boot with no JWT configuration")
}
