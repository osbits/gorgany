package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	b64 "encoding/base64"
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
)

const (
	// csrfTokenKey is the session item the token lives in. It is core.CSRFSessionKey
	// rather than a second literal, because this constant and the identical one in
	// http/middleware were independent declarations of the same string: changing
	// either would have left the middleware comparing against a key nothing wrote.
	csrfTokenKey    = core.CSRFSessionKey
	csrfTokenLength = 32 // 32 bytes = 256 bits
)

type CsrfService struct {
}

// GenerateCSRFToken generates a new CSRF token and stores it in the session
func (thiz *CsrfService) GenerateCSRFToken(ctx context.Context, session core.ISession) (string, error) {
	// Generate a cryptographically secure random token
	tokenBytes := make([]byte, csrfTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate CSRF token: %w", err)
	}

	// Encode the token as base64 using standard encoding
	token := b64.StdEncoding.EncodeToString(tokenBytes)

	// Store the token in the session
	session.SetItem(csrfTokenKey, token)

	// SetItem is void, so a database-backed session's write-through has nowhere to report a
	// failure and this used to return the token regardless — a token the store never
	// received. The client then holds one the server cannot match: every mutating request it
	// makes is refused by the CSRF check, and on a second replica the row carries a different
	// token entirely. The endpoint that does this is /csrf, registered by default and needing
	// no authentication, so the failure is reachable by anyone.
	if err := core.PendingWriteError(session); err != nil {
		return "", fmt.Errorf(
			"the CSRF token for session %s was not stored and will not be issued: %w",
			session.GetId(), err)
	}

	return token, nil
}

// ValidateCSRFToken validates a CSRF token against the one stored in the session
func (thiz *CsrfService) ValidateCSRFToken(ctx context.Context, session core.ISession, token string) bool {
	if token == "" {
		return false
	}

	// Get the token from the session
	sessionToken := session.GetItem(csrfTokenKey)
	if sessionToken == "" {
		return false
	}

	// Constant-time comparison, which the comment here has always claimed and
	// strings.Compare has never provided: it short-circuits on the first differing
	// byte, so the timing leaked the length of the matching prefix. CSRFMiddleware
	// does its own comparison and was fixed in v2; this method is the one a caller
	// reaches directly.
	return subtle.ConstantTimeCompare([]byte(sessionToken), []byte(token)) == 1
}

// GetCSRFToken returns the current CSRF token from the session or generates a new one if none exists
//
// The reuse branch deliberately does not consult PendingWriteError. It performs no write, and
// the token it returns is one the session genuinely holds; refusing because some *earlier*
// write failed would stop handing out a working token over an unrelated problem.
func (thiz *CsrfService) GetCSRFToken(ctx context.Context, session core.ISession) (string, error) {
	// Check if a token already exists in the session
	token := session.GetItem(csrfTokenKey)
	if token != "" {
		return token, nil
	}

	// Generate a new token if none exists
	return thiz.GenerateCSRFToken(ctx, session)
}
