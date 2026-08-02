package provider

import (
	"fmt"

	"github.com/osbits/gorgany/v2/auth"
	"github.com/osbits/gorgany/v2/config"
	"github.com/spf13/viper"
)

// ValidateJwtConfig reports a JWT configuration that cannot be used safely.
//
// Nothing validated auth.jwt.secret anywhere before, in any execution mode. An app could
// boot — server or CLI, dev or prod — with the key missing from config.yaml, set to an empty
// string, set to a YAML null, set to whitespace, left as an unresolved placeholder, or set
// to something short enough to guess, with no error and no warning, and then sign and verify
// tokens with it. The outcome is a pre-authentication, attacker-chosen identity: whoever
// knows the key mints a token naming any user and role they like.
//
// Whether the app uses JWT at all is config.JwtIsConfigured's question; an app that declares
// no auth.jwt key is left alone. config.ResolveEnvPlaceholders registers a `${JWT_SECRET}`
// default for the secret when the section is declared, which is what makes "the section is
// there but the secret line is missing" arrive here as a key that is present rather than as
// a key nothing can see.
func ValidateJwtConfig() error {
	if !config.JwtIsConfigured() {
		return nil
	}

	if err := auth.ValidateJwtSecret(viper.GetString(config.JwtSecretKey)); err != nil {
		return fmt.Errorf(
			"this application configures JWT authentication (an auth.jwt section is present) "+
				"but %w. Set %s to at least %d bytes from a random source — "+
				"`openssl rand -base64 48` — or remove the auth.jwt section if this "+
				"application does not use token authentication",
			err, config.JwtSecretKey, auth.MinJwtSecretLength)
	}

	return nil
}
