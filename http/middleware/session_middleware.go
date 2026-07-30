package middleware

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/osbits/gorgany/v2/auth"

	"github.com/osbits/gorgany/v2/app/core"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/model"
)

// SessionKey is the context key for storing the session
const SessionKey = "session"

// SessionMiddleware provides session management functionality
type SessionMiddleware struct {
	// AuthContext is used to access the authentication strategy
	AuthContext core.IAuthContext `container:"inject"`
	// SessionStorage is used to store and retrieve sessions
	SessionStorage core.ISessionStorage `container:"inject"`

	csrfService *auth.CsrfService `container:"inject"`
}

// NewSessionMiddleware creates a new session middleware
func NewSessionMiddleware() *SessionMiddleware {
	return &SessionMiddleware{}
}

// Handle implements the middleware handler
func (thiz SessionMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		ctx := message.Context()

		// Skip session creation for OPTIONS requests
		if msgCtx, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
			if msgCtx.GetRequest().Method == http.MethodOptions {
				next(message)
				return
			}
		}

		// Get the authentication strategy
		strat := thiz.AuthContext.ResolveAuthStrategyByContext(ctx)
		if strat == nil {
			next(message)
			return
		}

		// Get the current session
		session := strat.CurrentSession(ctx)
		now := time.Now()

		// If we have a valid session, update it
		if session != nil && !session.IsExpired() {
			session.SetExpiry(now.Add(time.Duration(thiz.SessionStorage.GetSessionLifetime().Seconds()) * time.Second))
			thiz.markFlashUsed(session)
			session.SetLastActivity(time.Now())

			// Publish the token on this response too. This branch used to return
			// without it, so X-CSRF-Token appeared on exactly one response per
			// session — the one that created it. A client that missed that response
			// (a reload, a second tab, a session that already existed) had no way to
			// obtain a token, and after v2 hardened CSRFMiddleware every mutating
			// request it made was reliably rejected.
			thiz.publishCsrfToken(message, session)

			// Update the message context with the session
			// We can't directly update the message context, but the session will be available via the context

			next(message)
			return
		}

		// If we have an expired session, delete it
		if session != nil && session.IsExpired() {
			thiz.SessionStorage.DeleteSession(session)
		}

		// Create a new session
		newSess, err := strat.NewSessionWithoutUser(ctx)
		if err != nil {
			err2.HandleError(err)
			next(message)
			return
		}

		if newSess == nil {
			next(message)
			return
		}

		if editableSession, ok := message.Session().(core.IEditableSessionScope); ok {
			editableSession.Set(newSess)
		}

		csrfToken, e := thiz.csrfService.GenerateCSRFToken(message.Context(), newSess)
		if e != nil {
			err2.HandleError(e)
			next(message)
			return
		}
		message.Response().Header().Set(core.CSRFTokenHeader, csrfToken)

		thiz.SessionStorage.AddSession(newSess)

		// Update the message context with the session
		// We can't directly update the message context, but the session will be available via the context

		next(message)
	}
}

// publishCsrfToken puts the session's current CSRF token on the response, minting one
// if the session has none yet.
//
// It reuses the existing token rather than rotating on every request: rotating would
// invalidate the token any in-flight request from another tab is already carrying.
// A failure here is logged and ignored — the response itself is still valid, the
// client simply has to fetch the token from the endpoint instead.
func (thiz SessionMiddleware) publishCsrfToken(message core.HttpMessage, session core.ISession) {
	if thiz.csrfService == nil {
		return
	}

	token, e := thiz.csrfService.GetCSRFToken(message.Context(), session)
	if e != nil {
		err2.HandleError(e)
		return
	}

	message.Response().Header().Set(core.CSRFTokenHeader, token)
}

// markFlashUsed marks flash data as used
func (thiz *SessionMiddleware) markFlashUsed(session core.ISession) {
	raw := session.GetItem(core.OneTimeSessionAttributeKey)
	if raw == "" {
		return
	}

	otp := model.OneTimeParams{}
	if err := json.Unmarshal([]byte(raw), &otp); err != nil {
		err2.HandleError(err)
		return
	}

	otp.Start = false
	buf, err := json.Marshal(otp)
	if err != nil {
		err2.HandleError(err)
		return
	}

	session.SetItem(core.OneTimeSessionAttributeKey, string(buf))
}
