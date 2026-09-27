package azuread

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The token source over fake credentials, which count what they are asked and answer as each
// test says. The clock is the test's, so expiry is exact.

// fakeCredential is an azcore.TokenCredential that can also sign a person in, as interactive
// and device_code credentials can.
type fakeCredential struct {
	mu            sync.Mutex
	getTokens     int
	authenticates int
	scopes        [][]string

	// getToken answers the nth GetToken, counting from 1; nil hands out token-<n>, valid for an
	// hour from the clock.
	getToken func(ctx context.Context, n int) (azcore.AccessToken, error)
	// authenticate answers Authenticate; nil signs in at once.
	authenticate func(ctx context.Context) error
	// record is the record of the account a sign-in that works signs in as; zero is standInRecord.
	record azidentity.AuthenticationRecord
	clock  *clock
}

func (f *fakeCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.mu.Lock()
	f.getTokens++
	n := f.getTokens
	f.scopes = append(f.scopes, opts.Scopes)
	answer := f.getToken
	f.mu.Unlock()

	if answer != nil {
		return answer(ctx, n)
	}
	return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: f.clock.Now().Add(time.Hour)}, nil
}

func (f *fakeCredential) Authenticate(ctx context.Context, opts *policy.TokenRequestOptions) (azidentity.AuthenticationRecord, error) {
	f.mu.Lock()
	f.authenticates++
	f.scopes = append(f.scopes, opts.Scopes)
	answer, record := f.authenticate, f.record
	f.mu.Unlock()

	if answer != nil {
		return azidentity.AuthenticationRecord{}, answer(ctx)
	}
	if record == (azidentity.AuthenticationRecord{}) {
		record = standInRecord
	}
	return record, nil
}

func (f *fakeCredential) counts() (getTokens, authenticates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getTokens, f.authenticates
}

// silentCredential can only GetToken, as the Azure CLI's and a service principal's can.
type silentCredential struct{ fake *fakeCredential }

func (s silentCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return s.fake.GetToken(ctx, opts)
}

// clock is a settable time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

// newClock starts at the real time. A source the engine builds, as the datasource tests use,
// judges its tokens by time.Now, not by the clock that stamped them, so tokens stamped from a
// fixed date would all be expired once that date had passed.
func newClock() *clock { return &clock{now: time.Now()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// source is the token source of method against the Azure host over cred, on c's time, with a
// lifetime the test can end.
func source(t *testing.T, method string, cred azcore.TokenCredential, c *clock) (*cachingTokenSource, context.CancelFunc) {
	t.Helper()
	root, closeDatasource := context.WithCancel(context.Background())
	t.Cleanup(closeDatasource)

	req := request(method, azureHost, dsconfig.Auth{})
	req.Context = root
	s, err := newTokenSource(cred, req, req.Scope, nil)
	require.NoError(t, err)
	s.now = c.Now
	return s, closeDatasource
}

func token(t *testing.T, s *cachingTokenSource) string {
	t.Helper()
	tok, err := s.Token(context.Background())
	require.NoError(t, err)
	return tok
}

// settle waits for the acquisition s has in flight, if any, such as a renewal it started in the
// background, to finish.
func settle(t *testing.T, s *cachingTokenSource) {
	t.Helper()
	s.mu.Lock()
	a := s.inflight
	s.mu.Unlock()
	if a != nil {
		waitFor(t, a.done, "the acquisition in flight")
	}
}

// waitFor fails t unless done closes within five seconds.
func waitFor[T any](t *testing.T, done <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-done:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within five seconds", what)
		var zero T
		return zero
	}
}

func TestTokenSourceCachesUntilNearExpiry(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c}
	s, _ := source(t, sqlserver.AuthMethodAzureCLI, cred, c)

	assert.Equal(t, "token-1", token(t, s))
	c.advance(54*time.Minute + 59*time.Second)
	assert.Equal(t, "token-1", token(t, s), "an hour's token is handed out for 55 minutes")

	c.advance(time.Second)
	assert.Equal(t, "token-2", token(t, s), "and renewed five minutes before it expires")
	getTokens, authenticates := cred.counts()
	assert.Equal(t, 2, getTokens)
	assert.Zero(t, authenticates, "the Azure CLI never asks a person")
}

func TestTokenSourceHonoursRefreshOn(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c, getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		return azcore.AccessToken{
			Token:     fmt.Sprintf("token-%d", n),
			ExpiresOn: c.Now().Add(time.Hour),
			RefreshOn: c.Now().Add(30 * time.Minute),
		}, nil
	}}
	s, _ := source(t, sqlserver.AuthMethodAzureDefault, cred, c)

	assert.Equal(t, "token-1", token(t, s))
	c.advance(29 * time.Minute)
	assert.Equal(t, "token-1", token(t, s))
	getTokens, _ := cred.counts()
	assert.Equal(t, 1, getTokens, "nothing is due before RefreshOn")

	c.advance(time.Minute)
	assert.Equal(t, "token-1", token(t, s), "at RefreshOn the token still serves while it is renewed")
	settle(t, s)
	getTokens, _ = cred.counts()
	assert.Equal(t, 2, getTokens, "the renewal started at RefreshOn, well before the expiry margin")
	assert.Equal(t, "token-2", token(t, s))
}

// TestAFailedEarlyRenewalKeepsTheValidToken: a managed identity's token lasts a day and is due
// at half of it, and an outage of the metadata service or Entra ID then must not fail any
// connection while the token has hours left. The renewal fails in the background, is not
// retried by every connection, and is tried again after retryAfter.
func TestAFailedEarlyRenewalKeepsTheValidToken(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c, getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		if n == 1 {
			return azcore.AccessToken{
				Token:     standInToken,
				ExpiresOn: c.Now().Add(24 * time.Hour),
				RefreshOn: c.Now().Add(12 * time.Hour),
			}, nil
		}
		return azcore.AccessToken{}, errors.New("IMDS unreachable")
	}}
	s, _ := source(t, sqlserver.AuthMethodAzureDefault, cred, c)
	assert.Equal(t, standInToken, token(t, s))

	c.advance(12*time.Hour + time.Minute)
	for range 20 {
		assert.Equal(t, standInToken, token(t, s), "11h59m of validity left")
		settle(t, s)
	}
	getTokens, _ := cred.counts()
	assert.Equal(t, 2, getTokens, "one renewal, which failed, and no second within retryAfter")

	c.advance(retryAfter)
	assert.Equal(t, standInToken, token(t, s))
	settle(t, s)
	getTokens, _ = cred.counts()
	assert.Equal(t, 3, getTokens, "tried again once retryAfter has passed")
}

// TestAStaleAnswerDoesNotRenewOnEveryConnection: MSAL answers a managed identity's refresh that
// failed with the cached token and its past RefreshOn, and no error. That answer is not fresh,
// so the next renewal waits retryAfter rather than starting with the next connection.
func TestAStaleAnswerDoesNotRenewOnEveryConnection(t *testing.T) {
	c := newClock()
	issued := c.Now()
	cred := &fakeCredential{clock: c, getToken: func(context.Context, int) (azcore.AccessToken, error) {
		return azcore.AccessToken{
			Token:     standInToken,
			ExpiresOn: issued.Add(24 * time.Hour),
			RefreshOn: issued.Add(12 * time.Hour),
		}, nil
	}}
	s, _ := source(t, sqlserver.AuthMethodAzureDefault, cred, c)
	assert.Equal(t, standInToken, token(t, s))

	c.advance(12*time.Hour + time.Minute)
	for range 20 {
		assert.Equal(t, standInToken, token(t, s))
		settle(t, s)
		c.advance(time.Second)
	}
	getTokens, _ := cred.counts()
	assert.Equal(t, 2, getTokens, "twenty connections over twenty seconds, one renewal")
}

// TestARenewalThatFailsInsideTheMarginHandsOutTheOldToken: within five minutes of the expiry a
// connection waits for the renewal, and when that fails with the token still good for a dial,
// the connection takes the token rather than an error. Past lastResort it gets the error.
func TestARenewalThatFailsInsideTheMarginHandsOutTheOldToken(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c, getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		if n == 1 {
			return azcore.AccessToken{Token: standInToken, ExpiresOn: c.Now().Add(time.Hour)}, nil
		}
		return azcore.AccessToken{}, errors.New("AADSTS50058: Entra ID is unreachable")
	}}
	s, _ := source(t, sqlserver.AuthMethodServicePrincipal, cred, c)
	assert.Equal(t, standInToken, token(t, s))

	c.advance(56 * time.Minute)
	assert.Equal(t, standInToken, token(t, s), "four minutes left: the renewal failed, the token serves")
	assert.Equal(t, standInToken, token(t, s))
	getTokens, _ := cred.counts()
	assert.Equal(t, 2, getTokens, "the second connection did not wait for another renewal within retryAfter")

	c.advance(retryAfter)
	assert.Equal(t, standInToken, token(t, s))
	getTokens, _ = cred.counts()
	assert.Equal(t, 3, getTokens, "and waited for the next one after it")

	c.advance(3*time.Minute - retryAfter)
	_, err := s.Token(context.Background())
	require.Error(t, err, "a minute left is too little for a dial")
	assert.Contains(t, err.Error(), "AADSTS50058")
	assert.NotContains(t, err.Error(), standInToken)
}

// TestAnExpiredOrEmptyTokenIsNeverHandedOut: a token without an expiry is not cached, and an
// empty one is an error rather than a login that fails at the server.
func TestAnExpiredOrEmptyTokenIsNeverHandedOut(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c, getToken: func(_ context.Context, n int) (azcore.AccessToken, error) {
		if n == 3 {
			return azcore.AccessToken{}, nil
		}
		return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n)}, nil
	}}
	s, _ := source(t, sqlserver.AuthMethodAzureCLI, cred, c)

	assert.Equal(t, "token-1", token(t, s))
	assert.Equal(t, "token-2", token(t, s))
	_, err := s.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the credential returned an empty token")
}

// TestConcurrentLoginsShareOneAcquisition: a pool opening fifty connections at once asks for
// fifty tokens, and the credential is asked once.
func TestConcurrentLoginsShareOneAcquisition(t *testing.T) {
	c := newClock()
	release := make(chan struct{})
	cred := &fakeCredential{clock: c}
	cred.getToken = func(ctx context.Context, n int) (azcore.AccessToken, error) {
		<-release
		return azcore.AccessToken{Token: fmt.Sprintf("token-%d", n), ExpiresOn: c.Now().Add(time.Hour)}, nil
	}
	s, _ := source(t, sqlserver.AuthMethodAzureCLI, cred, c)

	const logins = 50
	var started, finished sync.WaitGroup
	tokens := make(chan string, logins)
	for range logins {
		started.Add(1)
		finished.Add(1)
		go func() {
			defer finished.Done()
			started.Done()
			tok, err := s.Token(context.Background())
			assert.NoError(t, err)
			tokens <- tok
		}()
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond) // let them reach the acquisition; any that arrive later find the cache
	close(release)
	finished.Wait()
	close(tokens)

	for tok := range tokens {
		assert.Equal(t, "token-1", tok)
	}
	getTokens, _ := cred.counts()
	assert.Equal(t, 1, getTokens)
}

// TestWaitingLoginHonoursContextCancellation: a dial that gives up leaves at once, and the
// acquisition it started carries on for the next one.
func TestWaitingLoginHonoursContextCancellation(t *testing.T) {
	c := newClock()
	release := make(chan struct{})
	cred := &fakeCredential{clock: c}
	cred.getToken = func(ctx context.Context, n int) (azcore.AccessToken, error) {
		<-release
		return azcore.AccessToken{Token: "token-1", ExpiresOn: c.Now().Add(time.Hour)}, nil
	}
	s, _ := source(t, sqlserver.AuthMethodAzureCLI, cred, c)

	dial, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Token(dial)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Contains(t, err.Error(), "stopped waiting for the azure_cli token for "+standInTarget)

	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = s.Token(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	close(release)
	assert.Equal(t, "token-1", token(t, s))
	getTokens, _ := cred.counts()
	assert.Equal(t, 1, getTokens, "the one acquisition outlived the dials that gave up on it")
}

// TestInteractiveAuthenticatesOnceAtWarmUp: the person signs in on the first token, the warm-up's,
// and every renewal after it is silent.
func TestInteractiveAuthenticatesOnceAtWarmUp(t *testing.T) {
	for _, method := range []string{sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode} {
		t.Run(method, func(t *testing.T) {
			c := newClock()
			cred := &fakeCredential{clock: c}
			s, _ := source(t, method, cred, c)

			assert.Equal(t, "token-1", token(t, s))
			getTokens, authenticates := cred.counts()
			assert.Equal(t, 1, authenticates)
			assert.Equal(t, 1, getTokens, "Authenticate, then the token it cached")

			for range 3 {
				c.advance(time.Hour)
				token(t, s)
			}
			getTokens, authenticates = cred.counts()
			assert.Equal(t, 1, authenticates, "one sign-in per datasource")
			assert.Equal(t, 4, getTokens)
		})
	}
}

// TestInteractiveDoesNotPromptAfterWarmUp: once the person has signed in, a token azidentity
// cannot renew without them is an error saying what to do, and nobody is asked again.
func TestInteractiveDoesNotPromptAfterWarmUp(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c}
	cred.getToken = func(_ context.Context, n int) (azcore.AccessToken, error) {
		if n == 1 {
			return azcore.AccessToken{Token: standInToken, ExpiresOn: c.Now().Add(time.Hour)}, nil
		}
		return azcore.AccessToken{}, &azidentity.AuthenticationRequiredError{}
	}
	s, _ := source(t, sqlserver.AuthMethodInteractive, cred, c)
	assert.Equal(t, standInToken, token(t, s))

	c.advance(time.Hour)
	for range 2 {
		_, err := s.Token(context.Background())
		require.Error(t, err)
		assert.Equal(t, "azuread: the interactive sign-in for "+standInTarget+" could not be renewed silently: "+
			"its refresh token may have expired or been revoked, a Conditional Access policy may require a new "+
			"sign-in, or Entra ID may be unreachable; the next connection tries again, and if this persists, "+
			"restart the process to sign in again, or use auth.method: azure_cli", err.Error())
		assert.NotContains(t, err.Error(), standInToken)
	}
	getTokens, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "no second prompt")
	assert.Equal(t, 3, getTokens)
}

// TestAnInteractiveRenewalSaysWhatEndedIt: azidentity reports every failed silent renewal as an
// AuthenticationRequiredError, a timeout and a Close included, so the source asks what ended the
// renewal before it reads the error, and a renewal that fails with the token still good for a
// dial hands out the token.
func TestAnInteractiveRenewalSaysWhatEndedIt(t *testing.T) {
	// renewing signs in, then answers every renewal as azidentity does once the silent request
	// fails: with an AuthenticationRequiredError, whatever the cause.
	renewing := func(c *clock, entered chan<- struct{}) *fakeCredential {
		return &fakeCredential{clock: c, getToken: func(ctx context.Context, n int) (azcore.AccessToken, error) {
			if n == 1 {
				return azcore.AccessToken{Token: standInToken, ExpiresOn: c.Now().Add(time.Hour)}, nil
			}
			if entered != nil {
				entered <- struct{}{}
			}
			<-ctx.Done()
			return azcore.AccessToken{}, &azidentity.AuthenticationRequiredError{}
		}}
	}

	t.Run("its own timeout", func(t *testing.T) {
		c := newClock()
		s, _ := source(t, sqlserver.AuthMethodInteractive, renewing(c, nil), c)
		s.renewTimeout = 30 * time.Millisecond
		assert.Equal(t, standInToken, token(t, s))

		c.advance(time.Hour)
		_, err := s.Token(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "azuread: interactive could not obtain an access token for "+
			"https://database.windows.net/.default ("+standInTarget+") within 30ms")
		assert.NotContains(t, err.Error(), "raise auth.login_timeout", "login_timeout does not bound a renewal")
		assert.NotContains(t, err.Error(), "renewed silently")
	})

	t.Run("a Close", func(t *testing.T) {
		c := newClock()
		entered := make(chan struct{}, 1)
		s, closeDatasource := source(t, sqlserver.AuthMethodDeviceCode, renewing(c, entered), c)
		assert.Equal(t, standInToken, token(t, s))

		c.advance(time.Hour)
		result := make(chan error, 1)
		go func() {
			_, err := s.Token(context.Background())
			result <- err
		}()
		waitFor(t, entered, "the renewal")
		closeDatasource()
		err := waitFor(t, result, "the abandoned renewal's return")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "azuread: the device_code token request for "+standInTarget+
			" was abandoned: the datasource was closed")
		assert.NotContains(t, err.Error(), "renewed silently")
	})

	t.Run("with time left on the token", func(t *testing.T) {
		c := newClock()
		fake := renewing(c, nil)
		s, _ := source(t, sqlserver.AuthMethodInteractive, fake, c)
		s.renewTimeout = 30 * time.Millisecond
		assert.Equal(t, standInToken, token(t, s))

		c.advance(56 * time.Minute)
		assert.Equal(t, standInToken, token(t, s), "four minutes left serve a dial")
		getTokens, authenticates := fake.counts()
		assert.Equal(t, 2, getTokens, "the renewal was tried")
		assert.Equal(t, 1, authenticates, "and nobody was asked again")
	})
}

// TestAFailedSignInIsNotCountedAsOne: a sign-in that failed has signed nobody in, so the next
// token asks again, and still only when a token is needed.
func TestAFailedSignInIsNotCountedAsOne(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c}
	cred.authenticate = func(context.Context) error {
		if _, authenticates := cred.counts(); authenticates == 1 {
			return errors.New("the browser window was closed")
		}
		return nil
	}
	s, _ := source(t, sqlserver.AuthMethodInteractive, cred, c)

	_, err := s.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azuread: interactive could not obtain an access token for "+
		"https://database.windows.net/.default ("+standInTarget+"); if no browser can be opened "+
		"here, use auth.method: device_code: the browser window was closed")

	assert.Equal(t, "token-1", token(t, s))
	getTokens, authenticates := cred.counts()
	assert.Equal(t, 2, authenticates)
	assert.Equal(t, 1, getTokens)
}

// TestLazyInteractiveAuthenticationIsBoundedByLoginTimeout: with lazy_connect the sign-in runs
// under no caller's deadline, so its own bound is what ends a sign-in nobody finishes.
func TestLazyInteractiveAuthenticationIsBoundedByLoginTimeout(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c, authenticate: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	s, _ := source(t, sqlserver.AuthMethodInteractive, cred, c)
	s.loginTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := s.Token(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "azuread: the interactive sign-in for "+standInTarget+" did not "+
		"finish within 50ms; finish it sooner, or raise auth.login_timeout")
}

// TestTheLoginTimeoutDefaultsByMethod: the engine hands over login_timeout already defaulted, and
// a request built without one gets the same defaults.
func TestTheLoginTimeoutDefaultsByMethod(t *testing.T) {
	c := newClock()
	for method, want := range map[string]time.Duration{
		sqlserver.AuthMethodInteractive:      5 * time.Minute,
		sqlserver.AuthMethodDeviceCode:       5 * time.Minute,
		sqlserver.AuthMethodAzureCLI:         time.Minute,
		sqlserver.AuthMethodAzureDefault:     time.Minute,
		sqlserver.AuthMethodServicePrincipal: time.Minute,
		sqlserver.AuthMethodManagedIdentity:  2 * time.Minute,
		sqlserver.AuthMethodWorkloadIdentity: time.Minute,
	} {
		s, _ := source(t, method, &fakeCredential{clock: c}, c)
		assert.Equalf(t, want, s.loginTimeout, "method %s", method)
		assert.Equalf(t, time.Minute, s.renewTimeout, "method %s", method)
	}

	req := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{})
	req.LoginTimeout = 20 * time.Second
	s, err := newTokenSource(&fakeCredential{clock: c}, req, req.Scope, nil)
	require.NoError(t, err)
	assert.Equal(t, 20*time.Second, s.loginTimeout)
	assert.Equal(t, 20*time.Second, s.renewTimeout, "a renewal is never allowed longer than a sign-in")
}

// TestCloseAbandonsAnAcquisitionInFlight: the datasource's Close ends its lifetime, which ends a
// sign-in still waiting on a person, and a source that is closed asks for nothing more.
func TestCloseAbandonsAnAcquisitionInFlight(t *testing.T) {
	c := newClock()
	entered := make(chan struct{})
	cred := &fakeCredential{clock: c, authenticate: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	s, closeDatasource := source(t, sqlserver.AuthMethodInteractive, cred, c)

	result := make(chan error, 1)
	go func() {
		_, err := s.Token(context.Background())
		result <- err
	}()
	waitFor(t, entered, "the sign-in")
	closeDatasource()

	err := waitFor(t, result, "the abandoned sign-in's return")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the datasource was closed")
	require.ErrorIs(t, err, context.Canceled)

	_, err = s.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the datasource is closed")
	_, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "a closed source starts nothing")
}

// TestNoPromptOpensForAClosedDatasource: a Close that lands between the start of a sign-in and its
// prompt opens none, as a Close during the prompt ends it.
func TestNoPromptOpensForAClosedDatasource(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c}
	s, closeDatasource := source(t, sqlserver.AuthMethodDeviceCode, cred, c)
	closeDatasource()

	a := &acquisition{done: make(chan struct{})}
	s.acquire(a, true)
	require.Error(t, a.err)
	assert.Equal(t, "azuread: the device_code sign-in for "+standInTarget+" was abandoned: the datasource was "+
		"closed: context canceled", a.err.Error())
	getTokens, authenticates := cred.counts()
	assert.Zero(t, authenticates)
	assert.Zero(t, getTokens)
}

// TestTokenErrorsDoNotContainTheToken: a token is a bearer credential for the database, and an
// error ends up in a log.
func TestTokenErrorsDoNotContainTheToken(t *testing.T) {
	c := newClock()
	cred := &fakeCredential{clock: c}
	cred.getToken = func(ctx context.Context, n int) (azcore.AccessToken, error) {
		switch n {
		case 1:
			return azcore.AccessToken{Token: standInToken, ExpiresOn: c.Now().Add(time.Hour)}, nil
		case 2:
			return azcore.AccessToken{}, errors.New("AADSTS700082: the refresh token has expired")
		default:
			<-ctx.Done()
			return azcore.AccessToken{}, ctx.Err()
		}
	}
	s, _ := source(t, sqlserver.AuthMethodAzureDefault, cred, c)
	assert.Equal(t, standInToken, token(t, s))
	c.advance(time.Hour)

	_, err := s.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azuread: azure_default could not obtain an access token for "+
		"https://database.windows.net/.default ("+standInTarget+"): AADSTS700082")
	assert.NotContains(t, err.Error(), standInToken)

	// The third acquisition hangs until its own bound, which the dial does not wait for.
	s.loginTimeout = 20 * time.Millisecond
	dial, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = s.Token(dial)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), standInToken)

	_, err = s.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "within 20ms; raise auth.login_timeout if it needs longer")
	assert.NotContains(t, err.Error(), standInToken)
}

// TestTheScopeIsPassedThrough: the scope the engine resolved from the host, or auth.scope, is
// what the credential is asked for, by Authenticate and by GetToken.
func TestTheScopeIsPassedThrough(t *testing.T) {
	c := newClock()
	for name, tc := range map[string]struct {
		req  sqlserver.AuthRequest
		want string
	}{
		"public":         {request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{}), "https://database.windows.net/.default"},
		"US Government":  {request(sqlserver.AuthMethodInteractive, usGovHost, dsconfig.Auth{}), "https://database.usgovcloudapi.net/.default"},
		"an override":    {request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{Scope: "https://sql.example.com"}), "https://sql.example.com/.default"},
		"unresolved, CN": {withoutScope(request(sqlserver.AuthMethodInteractive, chinaHost, dsconfig.Auth{})), "https://database.chinacloudapi.cn/.default"},
	} {
		t.Run(name, func(t *testing.T) {
			cred := &fakeCredential{clock: c}
			saved := credentialFor
			credentialFor = func(sqlserver.AuthRequest, *rememberedSignIn) (azcore.TokenCredential, error) { return cred, nil }
			t.Cleanup(func() { credentialFor = saved })

			ts, err := authenticate(tc.req)
			require.NoError(t, err)
			_, err = ts.Token(context.Background())
			require.NoError(t, err)

			cred.mu.Lock()
			defer cred.mu.Unlock()
			assert.Equal(t, [][]string{{tc.want}, {tc.want}}, cred.scopes, "Authenticate's, then GetToken's")
		})
	}

	_, err := authenticate(withoutScope(request(sqlserver.AuthMethodInteractive, azureHost,
		dsconfig.Auth{Scope: "http://insecure.example.com"})))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.scope must be an https URL")
}

// withoutScope is req as a caller that resolved no scope builds it, which the package resolves
// itself.
func withoutScope(req sqlserver.AuthRequest) sqlserver.AuthRequest {
	req.Scope = ""
	return req
}

// TestInteractiveNeedsACredentialThatCanSignIn: a credential without Authenticate could only sign
// the person in by prompting from GetToken, which is what DisableAutomaticAuthentication forbids.
func TestInteractiveNeedsACredentialThatCanSignIn(t *testing.T) {
	req := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{})
	_, err := newTokenSource(silentCredential{&fakeCredential{clock: newClock()}}, req, req.Scope, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot ask for a sign-in")

	req = request(sqlserver.AuthMethodAzureCLI, azureHost, dsconfig.Auth{})
	_, err = newTokenSource(silentCredential{&fakeCredential{clock: newClock()}}, req, req.Scope, nil)
	require.NoError(t, err)
}
