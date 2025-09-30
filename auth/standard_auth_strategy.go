package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorganyio/gorgany/app/core"
	err2 "github.com/gorganyio/gorgany/err"
	"github.com/spf13/viper"
)

type StandardAuthStrategy struct {
	sessionManager core.ISessionStorage `container:"inject"`
	userService    core.IUserService    `container:"inject"`
	csrfService    *CsrfService         `container:"inject"`
	sessionFactory ISessionFactory      `container:"inject"`
}

func (thiz *StandardAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	uid := uuid.NewString()
	now := time.Now()

	// Generate cryptographically secure random bytes for session token
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Create a unique token by combining UUID, timestamp, and random bytes
	rawToken := fmt.Sprintf("%s%v%s", uid, now.UnixNano(), hex.EncodeToString(randomBytes))
	hashedTokenBytes := sha256.Sum256([]byte(rawToken))
	hashedToken := hex.EncodeToString(hashedTokenBytes[:])

	// Ensure the token is unique
	session := thiz.sessionManager.GetSessionById(hashedToken)
	if session != nil {
		return nil, fmt.Errorf("session creation failed: token collision detected")
	}

	// Create a new session with appropriate expiry time
	lifetime := thiz.sessionManager.GetSessionLifetime().Seconds()
	session = thiz.sessionFactory.CreateSession(hashedToken, now.Add(time.Duration(lifetime)*time.Second))
	thiz.sessionManager.AddSession(session)

	// Generate a CSRF token for the session
	_, err := thiz.csrfService.GenerateCSRFToken(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CSRF token: %w", err)
	}

	// Set the session cookie
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	// Set secure cookie with appropriate attributes
	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    session.GetId(),
		Path:     "/",
		MaxAge:   int(thiz.sessionManager.GetSessionLifetime().Seconds()), // Set explicit MaxAge to match session lifetime
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // Use StrictMode for better CSRF protection
		Domain:   viper.GetString("auth.session.cookie.domain"),
	})

	return session, nil
}

func (thiz *StandardAuthStrategy) ShouldRotateSession(session core.ISession) bool {
	if session == nil {
		return true
	}

	now := time.Now()

	// Don't rotate expired sessions
	if now.Sub(session.GetExpiry()) > 0 {
		return false
	}

	// Rotate if session has been inactive for too long
	if now.Sub(session.GetLastActivity()) > thiz.sessionManager.GetSessionActivityTimeout() {
		return true
	}

	// Force rotation every 24 hours
	if now.Sub(session.GetCreatedAt()) > thiz.sessionManager.GetSessionRotationInterval() {
		return true
	}

	return false
}

func (thiz *StandardAuthStrategy) RotateSession(ctx context.Context, oldSession core.ISession) (core.ISession, error) {
	// Create new session
	newSession, err := thiz.NewSessionWithoutUser(ctx)
	if err != nil {
		return nil, err
	}

	// If there was an old session, copy its user ID and attributes
	if oldSession != nil {
		newSession.SetUserId(oldSession.GetUserId())
		// Copy last activity time to maintain user activity tracking
		newSession.SetLastActivity(oldSession.GetLastActivity())
		// Delete the old session to prevent session accumulation
		thiz.sessionManager.DeleteSession(oldSession)
	}

	return newSession, nil
}

func (thiz *StandardAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	existingSession := thiz.CurrentSession(ctx)

	// If CurrentSession already rotated the session, we don't need to rotate again
	// If there's no existing session, we need to create one
	var session core.ISession
	var err error

	if existingSession == nil {
		// Create a new session if none exists
		session, err = thiz.NewSessionWithoutUser(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		// Use the existing session (which might have been rotated in CurrentSession)
		session = existingSession
	}

	session.SetUserId(user.GetId())
	session.SetLastActivity(time.Now())

	return session, nil
}

// IsLoggedIn
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) IsLoggedIn(ctx context.Context) bool {
	session := thiz.CurrentSession(ctx)
	if session == nil {
		return false
	}

	if session.IsExpired() {
		thiz.sessionManager.DeleteSession(session)
		return false
	}

	if session.GetUserId() == "" {
		return false
	}

	return true
}

// Logout
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) Logout(ctx context.Context) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return
	}

	// Delete the session from storage
	thiz.sessionManager.DeleteSessionById(thiz.ResolveSessionId(ctx))

	// Invalidate the session cookie with the same security settings as when creating it
	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // Negative value means delete cookie immediately
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode, // Use StrictMode for better CSRF protection
		Domain:   viper.GetString("auth.session.cookie.domain"),
	})
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	session := thiz.sessionManager.GetSessionById(thiz.ResolveSessionId(ctx))
	if session == nil {
		return nil, nil
	}

	// Add consistent session validation
	if session.IsExpired() {
		thiz.sessionManager.DeleteSession(session)
		return nil, nil
	}

	if session.GetUserId() == "" {
		return nil, nil
	}

	return thiz.userService.Get(session.GetUserId())
}

func (thiz *StandardAuthStrategy) ResolveSessionId(ctx context.Context) string {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return ""
	}

	// in cases where we make the first request and the cookie is not yet set, to avoid creating many empty sessions,
	// we can take the session id from the message context since we already have a session instance in the message.
	if messageContext.GetSession() != nil {
		return messageContext.GetSession().GetId()
	}

	cookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName)
	if cookie == nil {
		return ""
	}

	return cookie.Value
}

func (thiz *StandardAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	session := thiz.sessionManager.GetSessionById(thiz.ResolveSessionId(ctx))

	if session == nil {
		return nil
	}

	// Check if session needs rotation
	if thiz.ShouldRotateSession(session) {
		newSession, err := thiz.RotateSession(ctx, session)
		if err != nil {
			err2.HandleError(err)
			return nil
		}
		return newSession
	}

	return session
}

func (thiz *StandardAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	return thiz.ResolveSessionId(ctx) != ""
}
