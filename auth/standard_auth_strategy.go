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
	"github.com/osbits/gorgany/v2/app/core"
	err2 "github.com/osbits/gorgany/v2/err"
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

	// Ensure the token is unique. A lookup that failed is not evidence of uniqueness, so
	// it refuses rather than assuming the id is free.
	session, err := thiz.sessionManager.GetSessionById(hashedToken)
	if err != nil {
		return nil, fmt.Errorf("session creation failed: could not check the new id: %w", err)
	}
	if session != nil {
		return nil, fmt.Errorf("session creation failed: token collision detected")
	}

	// Create a new session with appropriate expiry time
	lifetime := thiz.sessionManager.GetSessionLifetime().Seconds()
	session = thiz.sessionFactory.CreateSession(hashedToken, now.Add(time.Duration(lifetime)*time.Second))
	if err := thiz.sessionManager.AddSession(session); err != nil {
		// Returning the session anyway would hand out a cookie for state no store holds:
		// every subsequent request would find nothing and start again, and any login on
		// this session would be lost.
		return nil, fmt.Errorf("session creation failed: could not store the session: %w", err)
	}

	// Generate a CSRF token for the session
	if _, err := thiz.csrfService.GenerateCSRFToken(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to generate CSRF token: %w", err)
	}

	// Set the session cookie
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	// MaxAge is set explicitly to match the session lifetime, so the browser drops the cookie
	// at the same moment the store stops honouring it.
	messageContext.GetCookieManager().SetCookie(NewSessionCookie(
		session.GetId(),
		int(thiz.sessionManager.GetSessionLifetime().Seconds()),
		http.SameSiteLaxMode))

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

// RotateSession moves a session onto a fresh identifier.
//
// The old session is revoked *first*, and the rotation is abandoned if it turns out there
// was nothing to revoke. The order used to be the other way round — mint the new session,
// copy the user id across, then delete the old one — and that is a revocation bypass in
// slow motion: a request that loaded the session just before the user logged out would
// carry the logged-out user's identity onto a brand new row and receive its cookie, so the
// bypass outlived the logout, outlived a process restart, and was indistinguishable from a
// real login. Revoking first means the delete either wins the race, in which case the
// rotation proceeds from a session that was genuinely live, or finds nothing, in which case
// somebody else already revoked it and no identity is carried anywhere.
//
// The identity is also republished through the storage before this reports success, so a
// rotation whose write did not land fails instead of handing out a cookie for state no
// store holds.
//
// Three things cross onto the new identifier and nothing else does: the user id, the last
// activity stamp and the CSRF token. The token is carried because it is bound to the session
// and a client has no way to learn that its session was rotated underneath it —
// SessionMiddleware publishes X-CSRF-Token before the handler runs, so on a request that
// rotates, the header the client reads is the *old* token by construction. Minting a new one
// here therefore hands every already-rendered form and every in-flight client a token the
// next request rejects: a spurious 403 at the activity and rotation thresholds, both of which
// fire on a request the client did not ask to rotate anything on. Nothing else is copied,
// deliberately. Attributes on a pre-authentication session are chosen by whoever presented it,
// and carrying them across the authentication boundary would let an attacker who planted the
// session also plant its contents.
//
// Carrying the token is right for the rotations that happen on their own and wrong for the one
// Login performs, which is why Login replaces it afterwards rather than this method deciding
// per caller: see the note there.
func (thiz *StandardAuthStrategy) RotateSession(ctx context.Context, oldSession core.ISession) (core.ISession, error) {
	if oldSession == nil {
		return thiz.NewSessionWithoutUser(ctx)
	}

	userId := oldSession.GetUserId()
	lastActivity := oldSession.GetLastActivity()
	csrfToken := oldSession.GetItem(core.CSRFSessionKey)

	wasLive, err := thiz.revoke(oldSession.GetId())
	if err != nil {
		return nil, fmt.Errorf("could not revoke session %s before rotating it: %w",
			oldSession.GetId(), err)
	}
	if !wasLive {
		return nil, fmt.Errorf(
			"session %s was already revoked, so it will not be rotated", oldSession.GetId())
	}

	newSession, err := thiz.NewSessionWithoutUser(ctx)
	if err != nil {
		return nil, err
	}

	newSession.SetUserId(userId)
	// Copy last activity time to maintain user activity tracking
	newSession.SetLastActivity(lastActivity)

	// NewSessionWithoutUser has already minted a token for the new session; the old one
	// replaces it so that a token already in a client's hands keeps working.
	if csrfToken != "" {
		newSession.SetItem(core.CSRFSessionKey, csrfToken)
	}

	if err := thiz.sessionManager.AddSession(newSession); err != nil {
		return nil, fmt.Errorf("could not persist the rotated session: %w", err)
	}

	return newSession, nil
}

// revoke deletes a session and says whether the store held it.
//
// A storage that cannot answer that question (see core.ISessionRevoker) is taken at its
// word: the delete either succeeded or it did not, and a successful delete of a session
// nothing held is reported as live, which is the pre-existing behaviour rather than a new
// refusal aimed at third-party storages.
func (thiz *StandardAuthStrategy) revoke(id string) (bool, error) {
	if revoker, ok := thiz.sessionManager.(core.ISessionRevoker); ok {
		return revoker.RevokeSession(id)
	}

	if err := thiz.sessionManager.DeleteSessionById(id); err != nil {
		return false, err
	}
	return true, nil
}

// Login authenticates a user onto a session identifier the client has never seen.
//
// The rotation is the whole point. This used to take whatever CurrentSession returned and
// assign the user id to it, which means the identifier that authenticates after a login is
// one the client presented — and a client can be made to present an identifier somebody else
// chose. A cookie written by a sibling of the registrable domain, an XSS on the app, or a
// minute alone with an unlocked browser is enough to plant one; the victim then logs in on it
// and the attacker's copy of that identifier is authenticated as the victim, with nothing to
// notice and nothing to expire. Neither of the existing rotation triggers helped: they fire on
// inactivity and on age, and the victim's own page loads keep refreshing the activity stamp.
//
// So the identifier is replaced unconditionally, and the replacement fails closed. If the old
// session cannot be revoked, or the new one cannot be persisted, this returns an error and
// reports no session at all: a login that leaves the presented identifier alive is exactly the
// fixation being prevented, and one whose session was never stored authenticates for a single
// request and then silently stops.
//
// The caller must install the returned session into the request scope — the framework's own
// handler does it through http.PublishSession. Everything resolved earlier in the request,
// including the session middleware's copy, still refers to the identifier that has just been
// deleted, and the CSRF token on the response is the pre-authentication one until it is
// republished.
func (thiz *StandardAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	existingSession := thiz.CurrentSession(ctx)

	var session core.ISession
	var err error

	if existingSession == nil {
		// Nothing to rotate: a brand new identifier is a rotated one by definition.
		session, err = thiz.NewSessionWithoutUser(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		session, err = thiz.RotateSession(ctx, existingSession)
		if err != nil {
			return nil, fmt.Errorf(
				"could not rotate the session presented by the client before authenticating "+
					"user %s: %w", user.GetId(), err)
		}
	}

	session.SetUserId(user.GetId())
	session.SetLastActivity(time.Now())

	// The CSRF token does not cross the authentication boundary, even though RotateSession
	// carries it over every other rotation. Every secret a presented session held was chosen by
	// whoever presented it, and the token is the one that keeps its value after the identifier
	// has been replaced. For an app that opted out of the __Host- cookie name by configuring a
	// Domain, that is enough on its own: a sibling of the registrable domain plants a session
	// whose token it read from /csrf, the login moves the victim onto an identifier the sibling
	// cannot guess, and then the sibling — which is same-site, so SameSite=Lax sends the
	// victim's cookie on its POSTs — presents the token it already knows and the CSRF check
	// passes. Replacing the token here is what makes the rotation actually replace the session
	// rather than only its name.
	//
	// The reason RotateSession carries the token at all is that a client cannot learn its
	// session was rotated underneath it; that does not apply here, because a login is a request
	// the client made and http.PublishSession puts the new token on its response.
	if _, err := thiz.csrfService.GenerateCSRFToken(ctx, session); err != nil {
		return nil, fmt.Errorf(
			"could not issue a CSRF token for the session of user %s: %w", user.GetId(), err)
	}

	// SetUserId is void, so a database-backed session's write-through has nowhere to report
	// a failure and this used to return a nil error regardless: a login whose row was never
	// written was reported as successful, the caller set no cookie it could rely on, and the
	// user appeared logged in for exactly one request. Persisting through the storage is the
	// nearest enclosing call that *can* fail, and it is also a positive confirmation that
	// the user id reached the store rather than an assumption that it did.
	if err := thiz.sessionManager.AddSession(session); err != nil {
		return nil, fmt.Errorf("could not persist the session for user %s: %w", user.GetId(), err)
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
		if err := thiz.sessionManager.DeleteSession(session); err != nil {
			err2.HandleError(err)
		}
		return false
	}

	if session.GetUserId() == "" {
		return false
	}

	return true
}

// Logout revokes the server-side session and then expires the cookie.
//
// The order matters and so does the refusal. It used to delete through a void
// DeleteSessionById and expire the cookie unconditionally, which is fail-open in the most
// literal sense: the browser is told the session is over while the session is still in the
// store, so the user believes they have logged out — on a shared machine, or after noticing
// something wrong — and anybody holding a copy of the cookie stays authenticated. The
// cookie is now left in place when the revocation fails, because a client that has thrown
// its cookie away cannot ask us to try again, and the error is returned so the handler can
// tell the user the logout did not happen.
//
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) Logout(ctx context.Context) error {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	// Delete the session from storage
	sessionId := thiz.ResolveSessionId(ctx)
	if err := thiz.sessionManager.DeleteSessionById(sessionId); err != nil {
		return fmt.Errorf("could not revoke session %s: %w", sessionId, err)
	}

	// Invalidated through the same builder as the cookie that was issued: a browser only
	// removes a cookie when the name, path and domain all match, so an expiry built by hand
	// beside the issue site is one refactor away from leaving the cookie in place.
	messageContext.GetCookieManager().SetCookie(
		NewSessionCookie("", -1, http.SameSiteStrictMode))

	// Anything that resolved the caller's identity earlier in this request memoised it against
	// the session that has just been revoked, and the revocation does not disturb the copy of
	// that session still hanging off the message context — so the memo would keep answering
	// with the identity of the user who just logged out. A handler that renders anything
	// access-controlled after logging out would filter it for them rather than for an
	// anonymous caller. Dropping the memo sends the next resolution back to the store, which
	// no longer holds the session.
	if memo, isMemo := messageContext.(core.IRequestIdentityMemo); isMemo {
		memo.InvalidateIdentity()
	}

	return nil
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz *StandardAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	// A lookup that failed is reported rather than answered with "nobody": the two mean
	// very different things to a handler deciding whether to serve a request.
	session, err := thiz.sessionManager.GetSessionById(thiz.ResolveSessionId(ctx))
	if err != nil {
		return nil, fmt.Errorf("could not resolve the current session: %w", err)
	}
	if session == nil {
		return nil, nil
	}

	// Add consistent session validation
	if session.IsExpired() {
		if err := thiz.sessionManager.DeleteSession(session); err != nil {
			err2.HandleError(err)
		}
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
		if session := messageContext.GetSession(); session != nil {
			return session.GetId()
		}
		return ""
	}

	cookie := messageContext.GetCookieManager().GetCookie(SessionCookieName())
	if cookie == nil {
		return ""
	}

	return cookie.Value
}

// CurrentSession resolves the session this request carries, or nil.
//
// A failed lookup answers nil, which fails closed — the request is treated as carrying no
// session and SessionMiddleware starts a fresh, unauthenticated one — but it is reported
// rather than swallowed, because "the session store is unreachable" and "this visitor has no
// cookie" look identical from here and only one of them is normal.
func (thiz *StandardAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	session, err := thiz.sessionManager.GetSessionById(thiz.ResolveSessionId(ctx))
	if err != nil {
		err2.HandleError(err)
		return nil
	}

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
