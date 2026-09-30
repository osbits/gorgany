package azuread

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
)

// The durations the token source manages its cached token by.
//
// renewBefore is the hard margin, measured back from the token's expiry. Before it, a
// connection takes the cached token at once, and a renewal that is due runs in the background.
// Within it, a connection waits for the renewal. A connection takes its token before it dials
// and signs in with it once the handshake is done, so the margin covers a slow dial with room to
// spare; one whose token has expired by then asks again (see ExpiringToken). Microsoft's own
// libraries renew at the same five minutes.
//
// lastResort is how long the cached token must still be valid for a connection to take it after
// a renewal inside the hard margin failed. A minute still covers the dial, and failing a login
// the old token would have passed helps nobody.
//
// retryAfter is how long the source waits, while the cached token is still usable, before it
// tries again after a renewal that failed or that brought back a token already due. It is
// azcore's own pause between early refreshes. Without it, every connection opened during an
// outage of Entra ID or the instance metadata service would start a renewal of its own, and a
// managed identity would be asked on every connection once its RefreshOn has passed: MSAL
// answers a refresh it could not make with the cached token and its old, past RefreshOn.
const (
	renewBefore = 5 * time.Minute
	lastResort  = time.Minute
	retryAfter  = 30 * time.Second
)

// signInCredential is what interactive and device_code credentials offer besides GetToken: the
// call that asks the person, which azidentity keeps apart from GetToken when
// DisableAutomaticAuthentication is set.
type signInCredential interface {
	Authenticate(ctx context.Context, opts *policy.TokenRequestOptions) (azidentity.AuthenticationRecord, error)
}

// cachingTokenSource is the one sqlserver.TokenSource of one datasource, over its one credential.
//
// The engine asks for a token for every physical connection it opens, before it dials and under
// the context of the dial, and a pool of ten opening together asks ten times at once. azidentity
// keeps an in-memory cache per credential, but not every credential: the Azure CLI's runs `az` on
// every call. So the source keeps the token itself, and has one acquisition in flight at a time,
// which every caller that arrives meanwhile shares. It is a sqlserver.ExpiringTokenSource, so a
// connection whose token expires during its handshake asks again, and is answered from the cache
// or by a renewal, never by a prompt.
//
// A renewal being due and the token being unusable are different things. From the token's
// RefreshOn, which MSAL sets to half the lifetime of a long-lived token such as a managed
// identity's 24 hours, the source renews in the background and keeps handing out the token it
// has; a renewal that fails there is dropped and tried again after retryAfter. Only within
// renewBefore of the expiry does a connection wait for the renewal, and if that fails too while
// the token is still valid for lastResort, the connection takes the old token instead of the
// error. So an outage of Entra ID fails connections only once the token it issued has nearly
// run out, not from the moment it was due for renewal.
//
// The acquisition runs in a goroutine of its own, bounded by its own timeout, under the
// datasource's lifetime rather than any caller's context. A caller waits for it or for its own
// context, whichever ends first, so a dial that gives up leaves at once and a sign-in a person is
// halfway through is not cancelled because the query that started it had a short deadline: the
// next caller finds it still in flight, or finished. Close ends the lifetime, and with it the
// acquisition.
//
// No lock is held across a call to the credential, which may open a browser.
type cachingTokenSource struct {
	cred   azcore.TokenCredential
	opts   policy.TokenRequestOptions
	method string
	scope  string
	target string // host:port/db, as the engine's errors name the database

	// interactive is set for interactive and device_code: the first acquisition asks the person
	// with Authenticate, and no later one may.
	interactive bool
	// remembered is how an interactive method's sign-in outlives the process, and nil unless
	// auth.token_cache is persistent. With the record of an earlier sign-in, the first
	// acquisition asks the persistent cache before the person; a sign-in writes the record the
	// next process starts from.
	remembered *rememberedSignIn
	// loginTimeout bounds an acquisition that may take a person or a tool: every acquisition of
	// the other methods, and an interactive method's sign-in, its resume from the persistent cache
	// included.
	loginTimeout time.Duration
	// renewTimeout bounds what of an interactive method asks Entra ID and no one else: its
	// renewals after the sign-in, and, within the sign-in, its resume from the persistent cache.
	renewTimeout time.Duration

	// root is the datasource's lifetime, which its Close ends.
	root context.Context
	now  func() time.Time

	mu            sync.Mutex // guards the fields below
	tok           azcore.AccessToken
	authenticated bool
	inflight      *acquisition
	// retryAt is when a renewal may next start while the cached token is still usable: zero
	// after one that brought a fresh token, and retryAfter on from one that did not.
	retryAt time.Time
}

// acquisition is one request to the credential, which every caller that arrives while it runs
// waits for. tok and err are set before done is closed, and read only after.
type acquisition struct {
	done chan struct{}
	tok  azcore.AccessToken
	err  error
}

// newTokenSource wraps cred as the token source of the datasource req describes, remembering its
// sign-in in remembered when that is not nil.
func newTokenSource(cred azcore.TokenCredential, req sqlserver.AuthRequest, scope string, remembered *rememberedSignIn) (*cachingTokenSource, error) {
	interactive := asksAPerson(req.Method)
	if _, canSignIn := cred.(signInCredential); interactive && !canSignIn {
		return nil, fmt.Errorf("azuread: the %s credential for %s cannot ask for a sign-in", req.Method, target(req))
	}

	loginTimeout := req.LoginTimeout
	if loginTimeout <= 0 {
		switch {
		case interactive:
			loginTimeout = sqlserver.DefaultInteractiveLoginTimeout
		case req.Method == sqlserver.AuthMethodManagedIdentity:
			loginTimeout = sqlserver.DefaultManagedIdentityLoginTimeout
		default:
			loginTimeout = sqlserver.DefaultLoginTimeout
		}
	}
	root := req.Context
	if root == nil {
		root = context.Background()
	}

	return &cachingTokenSource{
		cred:         cred,
		opts:         policy.TokenRequestOptions{Scopes: []string{scope}},
		method:       req.Method,
		scope:        scope,
		target:       target(req),
		interactive:  interactive,
		remembered:   remembered,
		loginTimeout: loginTimeout,
		renewTimeout: min(loginTimeout, sqlserver.DefaultLoginTimeout),
		root:         root,
		now:          time.Now,
	}, nil
}

// asksAPerson reports whether method signs a person in, which interactive and device_code do,
// and every other method this package registers does not.
func asksAPerson(method string) bool {
	return method == sqlserver.AuthMethodInteractive || method == sqlserver.AuthMethodDeviceCode
}

// target names req's database as the engine's errors do: host:port/db, or host\instance/db,
// from req.Target, and host/db for a request built without it. It never carries a credential.
func target(req sqlserver.AuthRequest) string {
	if req.Target != "" {
		return req.Target
	}
	return req.Host + "/" + req.Database
}

var _ sqlserver.ExpiringTokenSource = (*cachingTokenSource)(nil)

// Token returns the cached token while it is usable, starting a renewal in the background once
// one is due, and otherwise joins, or starts, the one acquisition in flight and waits for it or
// for ctx. A renewal inside the hard margin that fails leaves the old token to a caller while it
// is still valid for lastResort.
func (s *cachingTokenSource) Token(ctx context.Context) (string, error) {
	tok, err := s.accessToken(ctx)
	return tok.Token, err
}

// ExpiringToken is Token with the token's expiry (see sqlserver.ExpiringTokenSource).
func (s *cachingTokenSource) ExpiringToken(ctx context.Context) (string, time.Time, error) {
	tok, err := s.accessToken(ctx)
	return tok.Token, tok.ExpiresOn, err
}

// accessToken is Token's token with its expiry.
func (s *cachingTokenSource) accessToken(ctx context.Context) (azcore.AccessToken, error) {
	if s.root.Err() != nil {
		return azcore.AccessToken{}, fmt.Errorf("azuread: no token for %s: the datasource is closed", s.target)
	}

	s.mu.Lock()
	now := s.now()
	switch {
	case s.tok.Token == "":
	case now.Before(refreshAt(s.tok)):
		tok := s.tok
		s.mu.Unlock()
		return tok, nil
	case usable(s.tok, now, renewBefore) || usable(s.tok, now, lastResort) && now.Before(s.retryAt):
		// Due, or inside the margin just after a renewal failed: the token still serves, and a
		// renewal runs in the background unless one is in flight or the last one failed too
		// recently.
		tok := s.tok
		if s.inflight == nil && !now.Before(s.retryAt) {
			s.startLocked()
		}
		s.mu.Unlock()
		return tok, nil
	}
	a := s.inflight
	if a == nil {
		a = s.startLocked()
	}
	s.mu.Unlock()

	select {
	case <-a.done:
		if a.err == nil {
			return a.tok, nil
		}
		if tok, ok := s.lastResortToken(); ok {
			return tok, nil
		}
		return azcore.AccessToken{}, a.err
	case <-ctx.Done():
		return azcore.AccessToken{}, fmt.Errorf("azuread: stopped waiting for the %s token for %s: %w",
			s.method, s.target, ctx.Err())
	}
}

// startLocked starts an acquisition, signing the person in first when an interactive method has
// not signed in yet, and makes it the one in flight. s.mu must be held.
func (s *cachingTokenSource) startLocked() *acquisition {
	a := &acquisition{done: make(chan struct{})}
	s.inflight = a
	go s.acquire(a, s.interactive && !s.authenticated)
	return a
}

// lastResortToken returns the cached token when it is still valid for lastResort and the
// datasource is open.
func (s *cachingTokenSource) lastResortToken() (azcore.AccessToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root.Err() != nil || !usable(s.tok, s.now(), lastResort) {
		return azcore.AccessToken{}, false
	}
	return s.tok, true
}

// refreshAt is when tok is due for renewal: its RefreshOn, when it names one before the hard
// margin, and otherwise renewBefore ahead of its expiry.
func refreshAt(tok azcore.AccessToken) time.Time {
	at := tok.ExpiresOn.Add(-renewBefore)
	if !tok.RefreshOn.IsZero() && tok.RefreshOn.Before(at) {
		at = tok.RefreshOn
	}
	return at
}

// usable reports whether tok is a token that is still valid at now for margin more.
func usable(tok azcore.AccessToken, now time.Time, margin time.Duration) bool {
	return tok.Token != "" && now.Before(tok.ExpiresOn.Add(-margin))
}

// acquire runs a, signing the person in first when signIn is set (see signIn), and publishes the
// result to its waiters and, when it is a token, to the cache.
func (s *cachingTokenSource) acquire(a *acquisition, signIn bool) {
	timeout := s.loginTimeout
	if s.interactive && !signIn {
		timeout = s.renewTimeout
	}
	ctx, cancel := context.WithTimeout(s.root, timeout)
	defer cancel()

	var (
		tok      azcore.AccessToken
		err      error
		signedIn bool
	)
	if signIn {
		tok, signedIn, err = s.signIn(ctx, timeout)
	} else {
		tok, err = s.request(ctx, timeout)
	}

	s.mu.Lock()
	if signedIn {
		s.authenticated = true
	}
	now := s.now()
	if err == nil {
		s.tok = tok
	}
	if err != nil || !now.Before(refreshAt(tok)) {
		// Nothing fresh came back: a failure, or a token already due, as MSAL returns when it
		// could not refresh. Hold off the next renewal while the cached token still serves.
		s.retryAt = now.Add(retryAfter)
	} else {
		s.retryAt = time.Time{}
	}
	s.inflight = nil
	s.mu.Unlock()

	a.tok, a.err = tok, err
	close(a.done)
}

// request asks the credential for a token under ctx, which timeout bounds: every acquisition of a
// method that asks no person, and an interactive method's renewals.
func (s *cachingTokenSource) request(ctx context.Context, timeout time.Duration) (azcore.AccessToken, error) {
	tok, err := s.cred.GetToken(ctx, s.opts)
	if err = checked(tok, err); err != nil {
		return azcore.AccessToken{}, s.failure(ctx, err, requesting, timeout)
	}
	return tok, nil
}

// signIn signs the person in under ctx, which timeout bounds, and returns the first token. It
// reports whether anyone is signed in, which stays set when only that token failed.
//
// Signing in means asking the person with Authenticate, unless the sign-in an earlier process
// remembered still answers without them (see resume). A resume cut short by a Close or by its own
// timeout asks nobody: once the silent request has found the authority, MSAL opens the browser
// before it looks at the context again, so a prompt after it would ask the person for a sign-in
// already abandoned. A sign-in the person completes writes its record for the next process.
func (s *cachingTokenSource) signIn(ctx context.Context, timeout time.Duration) (azcore.AccessToken, bool, error) {
	if s.remembered.hasRecord() {
		tok, resumed, err := s.resume(ctx, timeout)
		switch {
		case err != nil:
			return azcore.AccessToken{}, false, err
		case resumed:
			return tok, true, nil
		}
	}

	if err := ctx.Err(); err != nil {
		// Closed, or out of time, before the prompt: none opens.
		return azcore.AccessToken{}, false, s.failure(ctx, err, prompting, timeout)
	}
	opts := s.opts
	record, err := s.cred.(signInCredential).Authenticate(ctx, &opts)
	if err != nil {
		return azcore.AccessToken{}, false, s.failure(ctx, err, prompting, timeout)
	}
	if s.remembered != nil {
		s.remembered.save(record, s.target)
	}
	tok, err := s.cred.GetToken(ctx, s.opts)
	if err = checked(tok, err); err != nil {
		return azcore.AccessToken{}, true, s.failure(ctx, err, requesting, timeout)
	}
	return tok, true, nil
}

// resume signs in without the person, from the persistent cache, with the record of an earlier
// process's sign-in. resumed is false when the cache cannot answer: its refresh token expired or
// was revoked, a Conditional Access policy requires a new sign-in, or the credential store lost
// it. The caller then asks the person, as it would without a persistent cache, and that one prompt
// is still the process's only one.
//
// It is bounded like a renewal, by renewTimeout within what is left of ctx, since it too asks
// Entra ID and no one else, so a slow one leaves the person most of the sign-in's time. err is set
// when a Close or that bound cut it short, and says so; the caller then asks nobody.
//
// Why the cache could not answer is not kept: with DisableAutomaticAuthentication, azidentity
// reports every silent failure as an AuthenticationRequiredError, whatever its cause, and the
// sign-in that follows either works or says what is wrong.
func (s *cachingTokenSource) resume(ctx context.Context, timeout time.Duration) (tok azcore.AccessToken, resumed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.renewTimeout)
	defer cancel()
	tok, err = s.cred.GetToken(ctx, s.opts)
	switch {
	case err == nil && tok.Token != "":
		return tok, true, nil
	case ctx.Err() != nil:
		return azcore.AccessToken{}, false, s.failure(ctx, ctx.Err(), resuming, min(s.renewTimeout, timeout))
	}
	return azcore.AccessToken{}, false, nil
}

// checked is err, or an error for a token that is empty.
func checked(tok azcore.AccessToken, err error) error {
	if err == nil && tok.Token == "" {
		return errors.New("the credential returned an empty token")
	}
	return err
}

// stage is what an acquisition was doing when it failed, which its error says.
type stage int

const (
	// requesting is asking the credential for a token without the person: every acquisition of
	// a method that asks no person, an interactive method's renewals, and its token right after
	// the sign-in.
	requesting stage = iota
	// resuming is signing an interactive method in from the persistent cache, before anyone is
	// asked (see resume).
	resuming
	// prompting is asking the person.
	prompting
)

// failure explains an acquisition that failed with err at stage at; timeout is what bounded it.
//
// What ended the acquisition is asked first: a Close, then its own timeout. Only then does the
// error's kind count, because azidentity reports every failed silent renewal of an interactive
// method as an AuthenticationRequiredError once DisableAutomaticAuthentication is set, whatever
// the cause was, a timeout and a Close included, and drops the cause. So that case cannot say why
// the renewal failed, only that it did, what usually causes it, and that the answer to a lasting
// failure is a restart, or a method that renews without a person, never a prompt the process
// opens by itself in the middle of a query.
//
// The errors name the method, the scope and the database, and never the token.
func (s *cachingTokenSource) failure(ctx context.Context, err error, at stage, timeout time.Duration) error {
	var required *azidentity.AuthenticationRequiredError
	switch {
	case s.root.Err() != nil && at == resuming:
		return fmt.Errorf("azuread: resuming the %s sign-in for %s from the persistent token cache was "+
			"abandoned, and nobody was asked: the datasource was closed: %w", s.method, s.target, err)
	case s.root.Err() != nil:
		what := "token request"
		if at == prompting {
			what = "sign-in"
		}
		return fmt.Errorf("azuread: the %s %s for %s was abandoned: the datasource was closed: %w",
			s.method, what, s.target, err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && at == resuming:
		msg := fmt.Sprintf("azuread: the %s sign-in for %s could not be resumed from the persistent token "+
			"cache within %s, so nobody was asked; Entra ID may be unreachable, and the next connection "+
			"tries again", s.method, s.target, timeout)
		if timeout == s.loginTimeout {
			msg += "; raise auth.login_timeout if it needs longer"
		}
		return fmt.Errorf("%s: %w", msg, err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && at == prompting:
		return fmt.Errorf("azuread: the %s sign-in for %s did not finish within %s; finish it sooner, "+
			"or raise auth.login_timeout: %w", s.method, s.target, timeout, err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		msg := fmt.Sprintf("azuread: %s could not obtain an access token for %s (%s) within %s",
			s.method, s.scope, s.target, timeout)
		if timeout == s.loginTimeout {
			// An interactive method's renewals are bounded by renewTimeout, which a larger
			// login_timeout does not raise.
			msg += "; raise auth.login_timeout if it needs longer"
		}
		return fmt.Errorf("%s: %w", msg, err)
	case s.interactive && at == requesting && errors.As(err, &required):
		return fmt.Errorf("azuread: the %s sign-in for %s could not be renewed silently: its refresh token "+
			"may have expired or been revoked, a Conditional Access policy may require a new sign-in, or "+
			"Entra ID may be unreachable; the next connection tries again, and if this persists, restart "+
			"the process to sign in again, or use auth.method: azure_cli", s.method, s.target)
	}

	msg := fmt.Sprintf("azuread: %s could not obtain an access token for %s (%s)", s.method, s.scope, s.target)
	if s.method == sqlserver.AuthMethodInteractive {
		msg += "; if no browser can be opened here, use auth.method: device_code"
	}
	return fmt.Errorf("%s: %w", msg, err)
}
