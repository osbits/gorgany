package auth

import (
	"context"
	"crypto/rand"
	b64 "encoding/base64"
	"fmt"
	"github.com/gorganyio/gorgany/app/core"
	str "strings"
)

const (
	csrfTokenKey    = "csrf_token"
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

	// Use constant-time comparison to prevent timing attacks
	return str.Compare(sessionToken, token) == 0
}

// GetCSRFToken returns the current CSRF token from the session or generates a new one if none exists
func (thiz *CsrfService) GetCSRFToken(ctx context.Context, session core.ISession) (string, error) {
	// Check if a token already exists in the session
	token := session.GetItem(csrfTokenKey)
	if token != "" {
		return token, nil
	}

	// Generate a new token if none exists
	return thiz.GenerateCSRFToken(ctx, session)
}
