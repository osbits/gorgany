package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"github.com/google/uuid"
	"net/http"
	"time"
)

func GetAuthStrategy(strategyName ...string) core.IAuthStrategy {
	return internal.GetFrameworkRegistrar().GetAuthStrategy(strategyName...)
}

type StandardAuthStrategy struct {
	sessionManager core.ISessionStorage `container:"inject"`
}

func (thiz *StandardAuthStrategy) NewSessionWithoutUser(ctx context.Context) (string, error) {
	uid := uuid.NewString()
	now := time.Now()

	rawToken := fmt.Sprintf("%s%v", uid, now.UnixNano())
	hashedTokenBytes := md5.Sum([]byte(rawToken))
	hashedToken := hex.EncodeToString(hashedTokenBytes[:])

	session := thiz.sessionManager.GetSessionById(hashedToken)
	if session != nil {
		return "", fmt.Errorf("Session %s already exists", hashedToken)
	}

	session = &Session{
		id:     hashedToken,
		expiry: now.Add(time.Second * time.Duration(internal.GetFrameworkRegistrar().GetSessionLifetime())),
	}
	thiz.sessionManager.AddSession(session)

	return session.GetId(), nil
}

func (thiz *StandardAuthStrategy) Login(user core.Authenticable, ctx context.Context) (string, error) {
	session := thiz.CurrentSession(ctx)

	sessionKey := session.GetId()
	if session == nil || (session.GetUsername() != "" && !session.IsExpired()) {
		var err error
		sessionKey, err = thiz.NewSessionWithoutUser(ctx)
		if err != nil {
			return "", err
		}
		session = thiz.sessionManager.GetSessionById(sessionKey)
	}

	session.SetUsername(user.GetUsername())

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return "", fmt.Errorf("Ctx is not core.IMessageContext instance")
	}
	if cookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName); cookie == nil {
		cookie = &http.Cookie{
			Name:     core.SessionCookieName,
			Value:    sessionKey,
			Path:     "/",
			MaxAge:   0,
			Secure:   true,
			HttpOnly: true,
		}

		messageContext.GetCookieManager().SetCookie(cookie)
	}

	return sessionKey, nil
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

	if session.GetUsername() == "" {
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

	thiz.sessionManager.DeleteSessionById(thiz.GetSessionId(ctx))

	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
	})
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	session := thiz.sessionManager.GetSessionById(thiz.GetSessionId(ctx))
	if session == nil {
		return nil, nil
	}

	if session.IsExpired() || session.GetUsername() == "" {
		return nil, nil
	}

	return GetAuthEntityService().GetByUsername(session.GetUsername())
}

func (thiz *StandardAuthStrategy) GetCurrentOrCreateSession(ctx context.Context) core.ISession {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return nil
	}

	session := thiz.CurrentSession(ctx)

	if session != nil && !session.IsExpired() {
		session.SetExpiry(session.GetExpiry().Add(time.Duration(internal.GetFrameworkRegistrar().GetSessionLifetime()) * time.Second))
		return session
	}

	if session != nil && session.IsExpired() {
		thiz.sessionManager.DeleteSession(session)
	}

	sessionKey, err := thiz.NewSessionWithoutUser(ctx)
	if err != nil {
		err2.HandleError(err)
		return nil
	}

	session = thiz.sessionManager.GetSessionById(sessionKey)

	messageContext.GetCookieManager().SetCookie(&http.Cookie{
		Name:     core.SessionCookieName,
		Value:    sessionKey,
		Path:     "/",
		MaxAge:   0,
		Secure:   true,
		HttpOnly: true,
	})

	return session
}

func (thiz *StandardAuthStrategy) GetSessionId(ctx context.Context) string {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return ""
	}

	cookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName)
	if cookie == nil {
		return ""
	}

	return cookie.Value
}

func (thiz *StandardAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	return thiz.sessionManager.GetSessionById(thiz.GetSessionId(ctx))
}

func (thiz *StandardAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	return thiz.GetSessionId(ctx) != ""
}
