package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

const (
	sessionRotationInterval = 24 * time.Hour
	sessionActivityTimeout  = 30 * time.Minute
)

type StandardAuthStrategy struct {
	sessionManager core.ISessionStorage `container:"inject"`
	userService    core.IUserService    `container:"inject"`
}

func (thiz *StandardAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	uid := uuid.NewString()
	now := time.Now()

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}

	rawToken := fmt.Sprintf("%s%v%s", uid, now.UnixNano(), hex.EncodeToString(randomBytes))
	hashedTokenBytes := sha256.Sum256([]byte(rawToken))
	hashedToken := hex.EncodeToString(hashedTokenBytes[:])

	session := thiz.sessionManager.GetSessionById(hashedToken)
	if session != nil {
		return nil, fmt.Errorf("session creation failed")
	}

	session = &Session{
		id:           hashedToken,
		expiry:       now.Add(time.Second * thiz.sessionManager.GetSessionLifetime()),
		createdAt:    now,
		lastActivity: now,
	}
	thiz.sessionManager.AddSession(session)

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}
	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    session.GetId(),
		Path:     "/",
		MaxAge:   0,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
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
	if now.Sub(session.GetLastActivity()) > sessionActivityTimeout {
		return true
	}

	// Force rotation every 24 hours
	if now.Sub(session.GetCreatedAt()) > sessionRotationInterval {
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

	// If there was an old session, copy its user ID
	if oldSession != nil {
		newSession.SetUserId(oldSession.GetUserId())
		// Delete the old session
		thiz.sessionManager.DeleteSession(oldSession)
	}

	return newSession, nil
}

func (thiz *StandardAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	// Always create a new session on login
	session, err := thiz.NewSessionWithoutUser(ctx)
	if err != nil {
		return nil, err
	}

	session.SetUserId(user.GetId())
	session.SetLastActivity(time.Now())

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	cookie := &http.Cookie{
		Name:     core.SessionCookieName,
		Value:    session.GetId(),
		Path:     "/",
		MaxAge:   0,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
		Domain:   viper.GetString("auth.session.cookie.domain"),
	}

	messageContext.GetCookieManager().SetCookie(cookie)

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

	thiz.sessionManager.DeleteSessionById(thiz.ResolveSessionId(ctx))

	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
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
