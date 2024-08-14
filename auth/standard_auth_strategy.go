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
	"github.com/spf13/viper"
	"math/rand"
	"net/http"
	"time"
)

type StandardAuthStrategy struct {
	sessionManager core.ISessionStorage `container:"inject"`
}

func (thiz *StandardAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	uid := uuid.NewString()
	now := time.Now()

	rand.Seed(now.UnixNano())

	rawToken := fmt.Sprintf("%s%v%d", uid, now.UnixNano(), rand.Intn(10000000))
	hashedTokenBytes := md5.Sum([]byte(rawToken))
	hashedToken := hex.EncodeToString(hashedTokenBytes[:])

	session := thiz.sessionManager.GetSessionById(hashedToken)
	if session != nil {
		return nil, fmt.Errorf("session %s already exists", hashedToken)
	}

	session = &Session{
		id:     hashedToken,
		expiry: now.Add(time.Second * time.Duration(internal.GetApplicationContext().GetSessionLifetime())),
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

func (thiz *StandardAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	session := thiz.CurrentSession(ctx)

	if session == nil || (session.GetUserId() != "" && !session.IsExpired()) {
		var err error
		session, err = thiz.NewSessionWithoutUser(ctx)
		if err != nil {
			return nil, err
		}
	}

	session.SetUserId(user.GetId())

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}
	if cookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName); cookie == nil {
		cookie = &http.Cookie{
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
	}

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

	if session.IsExpired() || session.GetUserId() == "" {
		return nil, nil
	}

	return GetAuthEntityService().Get(session.GetUserId())
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
	return thiz.sessionManager.GetSessionById(thiz.ResolveSessionId(ctx))
}

func (thiz *StandardAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	return thiz.ResolveSessionId(ctx) != ""
}
