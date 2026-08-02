package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is about the login POST arriving on a session that is *already* authenticated.
//
// The interesting case is not a user logging in twice. It is a session somebody else planted:
// an attacker who can write the session cookie into the victim's browser — a sibling of the
// registrable domain for an app that opted out of the __Host- cookie name, an XSS, a minute
// alone with the browser — plants their own logged-in session rather than a blank one. If the
// login handler answers an authenticated POST with a redirect, the victim's credentials are
// never looked at, the victim is left inside the attacker's account, and everything they type
// from then on is typed into it. Every request looks perfectly normal from the server's side.
//
// So the property under test is: a POST carrying credentials authenticates those credentials,
// whatever session it arrives on, and the session it leaves with belongs to the poster.

// multiUserService is a user directory with more than one entry. GetByUsername answers the
// credential check, Get answers CurrentUser's lookup by id.
type multiUserService struct {
	core.IUserService
	users map[string]core.Authenticable
}

func newMultiUserService(usersByUsername map[string]core.Authenticable) *multiUserService {
	return &multiUserService{users: usersByUsername}
}

func (s *multiUserService) GetByUsername(username string) (core.Authenticable, error) {
	user, ok := s.users[username]
	if !ok {
		return nil, nil
	}
	return user, nil
}

func (s *multiUserService) Get(id any) (core.Authenticable, error) {
	wanted := fmt.Sprint(id)
	for _, user := range s.users {
		if user.GetId() == wanted {
			return user, nil
		}
	}
	return nil, nil
}

// twoPartyPipeline wires a login pipeline holding an attacker and a victim with the same
// password, so that a POST is identified by *whose* username it carries.
func twoPartyPipeline(t *testing.T, backend e2eBackend) *loginPipeline {
	t.Helper()

	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	return newLoginPipelineWithUserService(t, backend, newMultiUserService(
		map[string]core.Authenticable{
			"victim@example.com":   &stubUser{id: "victim-42", password: password},
			"attacker@example.com": &stubUser{id: "attacker-7", password: password},
		}))
}

// loginAs runs one login POST for username and returns the cookie the client is left with.
func loginAs(
	t *testing.T, pipeline *loginPipeline, username, password string, presented *http.Cookie,
) (*http.Cookie, core.ISession) {
	t.Helper()

	body := "username=" + username + "&password=" + password

	var cookies []*http.Cookie
	if presented != nil {
		cookies = append(cookies, presented)
	}

	message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login", body, cookies...)
	pipeline.middleware.Handle(pipeline.controller.Login)(message)

	return lastSessionCookie(t, writer.Result().Cookies()), message.Session().Get()
}

// TestLoginOnAnAuthenticatedSessionAuthenticatesThePoster.
//
// The handler used to answer an authenticated POST with a redirect home, which means the posted
// credentials were never compared to anything. Whoever the session already belonged to stayed
// its owner, and the poster was silently handed that identity.
func TestLoginOnAnAuthenticatedSessionAuthenticatesThePoster(t *testing.T) {
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := twoPartyPipeline(t, backend)

			// Somebody is already logged in on this browser.
			held, _ := loginAs(t, pipeline, "attacker@example.com", "correct-horse",
				pipeline.startSession(t))
			require.NotNil(t, held)

			// And now a login POST arrives on it, with a different account's credentials.
			issued, current := loginAs(t, pipeline, "victim@example.com", "correct-horse", held)

			require.NotNil(t, current, "the request must end with a session")
			assert.Equal(t, "victim-42", current.GetUserId(),
				"the POST's own credentials decide who the session belongs to")

			require.NotNil(t, issued, "and the poster has to be given that session's cookie")
			assert.NotEqual(t, held.Value, issued.Value,
				"the identifier the poster arrived on must not be the one it leaves with")

			stored, err := pipeline.storage.GetSessionById(issued.Value)
			require.NoError(t, err)
			require.NotNil(t, stored, "the session the client leaves with must be in the store")
			assert.Equal(t, "victim-42", stored.GetUserId())

			gone, err := pipeline.storage.GetSessionById(held.Value)
			require.NoError(t, err)
			assert.Nil(t, gone, "and the session that was there before must have been revoked")
		})
	}
}

// TestAPlantedAuthenticatedSessionIsNotDonatedToTheVictim is the same fix seen from the
// attacker's side, end to end.
//
// The attacker logs in, plants their live cookie in the victim's browser, and waits. The victim
// logs in as themselves. Afterwards the victim must be themselves — in the request that logged
// them in and in the one after it — and the attacker's copy of the cookie must be worth nothing.
// Before the fix the victim's POST bounced off the redirect, so the victim spent the rest of the
// visit inside attacker-7's account while the attacker kept a live cookie for the same session
// and could watch, and act on, whatever the victim did there.
func TestAPlantedAuthenticatedSessionIsNotDonatedToTheVictim(t *testing.T) {
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := twoPartyPipeline(t, backend)

			planted, _ := loginAs(t, pipeline, "attacker@example.com", "correct-horse",
				pipeline.startSession(t))
			require.NotNil(t, planted)

			victimsCookie, duringLogin := loginAs(
				t, pipeline, "victim@example.com", "correct-horse", planted)

			require.NotNil(t, duringLogin)
			assert.Equal(t, "victim-42", duringLogin.GetUserId(),
				"the victim must not be browsing as the attacker in the login request itself")
			require.NotNil(t, victimsCookie)

			// The victim's next page load, carrying only what their browser now holds.
			next, _ := pipeline.newRequestMessage(t, http.MethodGet, "/home", "", victimsCookie)

			var whoTheVictimIs core.Authenticable
			pipeline.middleware.Handle(func(m core.HttpMessage) {
				whoTheVictimIs, _ = pipeline.strategy.CurrentUser(m.Context())
			})(next)

			require.NotNil(t, whoTheVictimIs, "the victim stays logged in after their login")
			assert.Equal(t, "victim-42", whoTheVictimIs.GetId(),
				"and as themselves, not as whoever planted the session")

			// The attacker keeps making requests with the identifier they planted.
			attackerRequest, _ := pipeline.newRequestMessage(t, http.MethodGet, "/home", "", planted)

			authenticated := true
			var served core.ISession
			pipeline.middleware.Handle(func(m core.HttpMessage) {
				authenticated = pipeline.strategy.IsLoggedIn(m.Context())
				served = m.Session().Get()
			})(attackerRequest)

			assert.False(t, authenticated,
				"the planted identifier must be dead once the victim has logged in")
			if served != nil {
				assert.NotEqual(t, planted.Value, served.GetId())
				assert.Empty(t, served.GetUserId())
			}
		})
	}
}

// TestABadPasswordOnALiveSessionLeavesItAlone pins the deliberate choice on the failure path.
//
// The credentials are checked — which is the fix — but a check that fails changes nothing. The
// alternative, ending the live session whenever a login attempt on it fails, would hand anyone
// who can get the victim's browser to POST /login a remote logout: no credentials needed, just
// a wrong password. Refusing the attempt and leaving the existing session where it is costs the
// caller nothing they cannot fix by typing the password correctly.
func TestABadPasswordOnALiveSessionLeavesItAlone(t *testing.T) {
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := twoPartyPipeline(t, backend)

			live, _ := loginAs(t, pipeline, "victim@example.com", "correct-horse",
				pipeline.startSession(t))
			require.NotNil(t, live)

			message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=victim@example.com&password=wrong-horse", live)
			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			assert.Equal(t, "/cp.login.show", writer.Result().Header.Get("Location"),
				"a rejected password must land back on the login form, not on the home page: "+
					"a POST that redirects home is one whose credentials were never checked")

			stored, err := pipeline.storage.GetSessionById(live.Value)
			require.NoError(t, err)
			require.NotNil(t, stored,
				"the session that was already live must survive a failed attempt on it")
			assert.Equal(t, "victim-42", stored.GetUserId(),
				"and must still belong to whoever it belonged to")

			assert.True(t, pipeline.strategy.IsLoggedIn(message.Context()),
				"the caller is still logged in as they were")
		})
	}
}

// TestAnUnknownUsernameOnALiveSessionLeavesItAlone — the same, for a username that does not
// resolve at all, which is a different branch of the handler.
func TestAnUnknownUsernameOnALiveSessionLeavesItAlone(t *testing.T) {
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := twoPartyPipeline(t, backend)

			live, _ := loginAs(t, pipeline, "victim@example.com", "correct-horse",
				pipeline.startSession(t))
			require.NotNil(t, live)

			message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=nobody@example.com&password=correct-horse", live)
			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			assert.Equal(t, "/cp.login.show", writer.Result().Header.Get("Location"))

			stored, err := pipeline.storage.GetSessionById(live.Value)
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(t, "victim-42", stored.GetUserId())
		})
	}
}

// TestShowLoginStillRedirectsAnAuthenticatedVisitor guards the other half of the change.
//
// Only the POST must stop redirecting. Sending an authenticated visitor who asks for the login
// *form* to the home page is right, and it is the one place the guard belongs — a GET carries no
// credentials, so there is nothing for it to verify and nothing to donate. This test passes both
// before and after the change by design: it exists so that removing the guard from Login does
// not take the one in ShowLogin with it.
//
// The view engine here is a nil-method double, so a ShowLogin that rendered would take the test
// down rather than quietly pass.
func TestShowLoginStillRedirectsAnAuthenticatedVisitor(t *testing.T) {
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := twoPartyPipeline(t, backend)

			live, _ := loginAs(t, pipeline, "victim@example.com", "correct-horse",
				pipeline.startSession(t))
			require.NotNil(t, live)

			message, writer := pipeline.newRequestMessage(t, http.MethodGet, "/login", "", live)
			pipeline.middleware.Handle(pipeline.controller.ShowLogin)(message)

			response := writer.Result()
			assert.Equal(t, "/home", response.Header.Get("Location"),
				"an authenticated visitor asking for the login form is sent home")
			assert.True(t, strings.HasPrefix(writer.Body.String(), `<a href="/home">`),
				"and the body is the redirect's own, with no login form appended to it: %q",
				writer.Body.String())

			current := message.Session().Get()
			require.NotNil(t, current)
			assert.Equal(t, live.Value, current.GetId(),
				"a GET that only redirects must not replace the session")
			assert.Equal(t, "victim-42", current.GetUserId())
		})
	}
}
