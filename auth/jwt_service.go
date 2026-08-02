package auth

import (
	"context"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"github.com/spf13/viper"
	"strings"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
	userService core.IUserService `container:"inject"`
}

// RoleClaim is the claim GenerateJwt writes the user's role into.
//
// It is informational, not authoritative. See RoleFromClaims.
const RoleClaim = "role"

// MinJwtSecretLength is the shortest auth.jwt.secret this package will sign or verify
// with, in bytes.
//
// 32 is the output size of SHA-256, which is the size RFC 2104 recommends for an HMAC key
// and the point past which lengthening it buys nothing. Below it the key is inside brute
// force or dictionary range, and the consequence of losing it is not "an attacker reads
// something" but "an attacker mints a token naming whatever user and role they like".
const MinJwtSecretLength = 32

// minDistinctJwtSecretBytes rejects a key that reached the length floor by repetition —
// `secret: aaaaaaaa…` padded to 32 characters is long and has no entropy at all. Eight is
// low enough that any real random key clears it, including hex, which has sixteen possible
// characters.
const minDistinctJwtSecretBytes = 8

// ValidateJwtSecret reports whether a string can serve as the HMAC key for this app's
// tokens.
//
// Nothing used to check. `token.SignedString([]byte(""))` succeeds, and jwt.Parse verifies
// against `[]byte("")` just as happily, so an app whose secret never arrived signed and
// accepted tokens with an empty key — and anyone can produce a valid signature under a key
// they know. `GenerateJwt(user, "")` returned a working token and a nil error. The config
// shapes that got there are ordinary: the key missing from config.yaml, `JWT_SECRET=` in a
// .env or a CI secret store, a literal `secret: ""`, a YAML null, or a placeholder the app
// substituted itself and left unresolved.
//
// The check lives here, not only in the boot validation, because JwtService is exported API
// an app can construct and call with a secret of its own choosing. It is cheap enough to
// run on the verification path: a length test and one pass over at most a few dozen bytes.
func ValidateJwtSecret(secret string) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf(
			"auth.jwt.secret is empty or whitespace only, so any token signed with it can be "+
				"forged by anyone; set it to at least %d bytes of cryptographically random "+
				"material", MinJwtSecretLength)
	}

	// An unresolved `${VAR}` reaching this far means the value was never substituted. It is
	// a shared, published string, so it is worse than a short random one.
	if strings.HasPrefix(secret, "${") && strings.HasSuffix(secret, "}") {
		return fmt.Errorf(
			"auth.jwt.secret is still the literal placeholder %q, so the environment variable "+
				"it names was never resolved; set that variable", secret)
	}

	if len(secret) < MinJwtSecretLength {
		return fmt.Errorf(
			"auth.jwt.secret is %d bytes, which is short enough to guess; it must be at least "+
				"%d bytes of cryptographically random material",
			len(secret), MinJwtSecretLength)
	}

	distinct := make(map[byte]struct{}, len(secret))
	for i := 0; i < len(secret); i++ {
		distinct[secret[i]] = struct{}{}
	}
	if len(distinct) < minDistinctJwtSecretBytes {
		return fmt.Errorf(
			"auth.jwt.secret is long enough but uses only %d distinct bytes, so it is padding "+
				"rather than random material; generate it with a random source",
			len(distinct))
	}

	return nil
}

func (thiz JwtService) GenerateJwt(user core.Authenticable, secret string) (string, error) {
	if err := ValidateJwtSecret(secret); err != nil {
		return "", fmt.Errorf("jwt: refusing to sign a token with an unusable key: %w", err)
	}

	token := jwt.New(jwt.SigningMethodHS256)
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("jwt: unexpected claims type %T on a freshly minted token", token.Claims)
	}

	jwtLifeTime := viper.GetInt("auth.jwt.lifeTime")
	claims["exp"] = time.Now().Add(time.Duration(jwtLifeTime) * time.Second).Unix()
	claims["username"] = user.GetUsername()
	claims[RoleClaim] = string(user.GetRole())

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

// RoleFromClaims reads the role claim from an already-verified token.
//
// The claim exists so a client can render its own UI — show an admin menu, hide a
// button — without a round trip, and so a log line can name the role without a lookup.
//
// It is deliberately **not** used for authorisation. A signed token is immutable for its
// whole lifetime, so a role baked into one outlives a role change until the token
// expires: demote an admin and they stay an admin for up to auth.jwt.lifeTime. The
// user-service lookup in JwtMiddleware remains the authority precisely because it sees
// the current role, which is what makes revocation work. That costs a lookup per
// role-guarded request, and the alternative costs correctness.
//
// An app that wants to skip the lookup can read this claim itself and accept the
// staleness window knowingly. The framework will not make that trade silently.
func RoleFromClaims(claims jwt.MapClaims) (core.UserRole, bool) {
	raw, present := claims[RoleClaim]
	if !present {
		return "", false
	}

	role, isString := raw.(string)
	if !isString || role == "" {
		return "", false
	}
	return core.UserRole(role), true
}

// jwtParseOptions pins the accepted signing method to the one GenerateJwt produces.
//
// It is worth being precise about what this does, because this option is usually described
// as the fix for algorithm confusion and here it is not. golang-jwt/jwt v5 already refuses
// every cross-family substitution, by three unrelated mechanisms: `None`, `NONE` and the
// other case variants name no registered signing method, so parsing fails before any key is
// consulted; `alg: none` does resolve, but its verifier demands the sentinel key
// jwt.UnsafeAllowNoneSignatureType, which the keyfunc below does not hand back; and an
// RS/PS/ES/EdDSA token fails the type assertion its own method performs on the []byte it is
// given. What this option does close is substitution *within* the HMAC family — HS384 and
// HS512 also take a []byte, so a token the framework signed as HS256 can be re-signed as
// HS384 against the same secret and verify. It also fences the day someone widens the
// keyfunc: the moment it can return an *rsa.PublicKey, that type assertion stops being the
// thing that saves us.
var jwtParseOptions = []jwt.ParserOption{jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()})}

func (thiz JwtService) ValidateJwt(token string, secret string) bool {
	// A key nothing can trust verifies nothing. Without this, a caller who knew the app's
	// secret was empty signed their own token and this returned true for it. ValidateJwt has
	// no way to report why, so the diagnosis is left to the boot validation and to
	// JwtMiddleware, which both name the key.
	if ValidateJwtSecret(secret) != nil {
		return false
	}

	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwtParseOptions...)

	if err != nil {
		return false
	}

	return t.Valid
}

func (thiz JwtService) ParseJwt(token string, secret string) (jwt.MapClaims, error) {
	if err := ValidateJwtSecret(secret); err != nil {
		return nil, fmt.Errorf("jwt: refusing to verify a token with an unusable key: %w", err)
	}

	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwtParseOptions...)
	if err != nil {
		return nil, err
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("jwt: unexpected claims type %T", t.Claims)
	}
	return claims, err
}

func (thiz JwtService) GetUser(token string, secret string) (core.Authenticable, error) {
	claims, err := thiz.ParseJwt(token, secret)
	if err != nil {
		return nil, err
	}

	// A validly-signed token whose `username` claim is absent or not a string used
	// to panic here. The claim set is attacker-influenced — anything that can obtain
	// a signature can choose the claims — so this must be a rejection, not a crash.
	username, ok := claims["username"].(string)
	if !ok {
		return nil, fmt.Errorf("jwt: token has no usable 'username' claim (got %T)", claims["username"])
	}
	if username == "" {
		return nil, fmt.Errorf("jwt: token has an empty 'username' claim")
	}

	if thiz.userService == nil {
		return nil, fmt.Errorf("jwt: no user service is wired, so a token cannot be resolved to a user")
	}

	return thiz.userService.GetByUsername(username)
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz JwtService) CurrentUser(ctx context.Context, secret string) (core.Authenticable, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Context is not IMessageContext instance")
	}

	token := util.ParseBearerToken(messageContext.GetHeader().Get("Authorization"))
	if token == "" {
		return nil, fmt.Errorf("User not found")
	}

	return thiz.GetUser(token, secret)
}
