package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This package had no tests, and it is the framework's own JWT login endpoint — the most
// probed route in any deployed app. Four defects in twenty lines: a panic on malformed JSON,
// a discarded body-read error, an unread error from Login followed by a nil dereference, and
// a nil strategy dereference. The last two are the same classes T3.3 and B1 fixed elsewhere.

// ------------------------------------------------------------------ doubles
//
// Each double embeds the interface it stands in for, so anything the handler reaches for that
// these do not model fails loudly rather than being silently accommodated.

type stubUser struct {
	core.Authenticable
	username string
	password string
}

func (u *stubUser) GetUsername() string { return u.username }
func (u *stubUser) GetPassword() string { return u.password }

type stubUserService struct {
	user core.Authenticable
	err  error
}

func (s *stubUserService) Get(any) (core.Authenticable, error) { return s.user, s.err }
func (s *stubUserService) GetByUsername(string) (core.Authenticable, error) {
	return s.user, s.err
}
func (s *stubUserService) Save(core.Authenticable) error { return nil }

type stubSession struct {
	core.ISession
	id string
}

func (s *stubSession) GetId() string { return s.id }

type stubStrategy struct {
	core.IAuthStrategy
	session core.ISession
	err     error
}

func (s *stubStrategy) Login(core.Authenticable, context.Context) (core.ISession, error) {
	return s.session, s.err
}

type stubAuthContext struct {
	core.IAuthContext
	strategies map[string]core.IAuthStrategy
}

func (c *stubAuthContext) Strategy(name ...string) core.IAuthStrategy {
	if len(name) == 0 {
		return c.strategies[""]
	}
	// A missing name yields a nil interface, which is exactly the case the handler has to
	// survive.
	return c.strategies[name[0]]
}

// ---------------------------------------------------------------- the message

type recorded struct {
	status int
	body   any
}

type stubResponse struct {
	core.IResponseScope
	rec *recorded
}

func (r *stubResponse) JSON(v any, code int) { r.rec.status, r.rec.body = code, v }

type stubRequest struct {
	core.IRequestScope
	raw     *http.Request
	readErr error
}

func (r *stubRequest) RawRequest() *http.Request { return r.raw }

func (r *stubRequest) Body() ([]byte, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	if r.raw.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.raw.Body)
}

type stubMessage struct {
	core.HttpMessage
	req *stubRequest
	res *stubResponse
	rec *recorded
}

func (m *stubMessage) Request() core.IRequestScope   { return m.req }
func (m *stubMessage) Response() core.IResponseScope { return m.res }
func (m *stubMessage) Context() context.Context      { return context.Background() }

func messageWithBody(body string) *stubMessage {
	rec := &recorded{}
	raw := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(body))
	return &stubMessage{
		req: &stubRequest{raw: raw},
		res: &stubResponse{rec: rec},
		rec: rec,
	}
}

// envelope decodes a recorded response the way a client would receive it.
func envelope(t *testing.T, body any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// controllerWith wires a controller whose login succeeds unless told otherwise.
func controllerWith(user core.Authenticable, strategy core.IAuthStrategy) LoginController {
	return LoginController{
		userService: &stubUserService{user: user},
		authContext: &stubAuthContext{strategies: map[string]core.IAuthStrategy{
			jwtStrategyName: strategy,
		}},
	}
}

// hashed returns a bcrypt hash of password, so the real comparison runs.
func hashed(t *testing.T, password string) string {
	t.Helper()

	h, err := util.HashWithSalt(password)
	require.NoError(t, err)
	return h
}

// ---------------------------------------------------------------------- tests

// TestMalformedJsonIsA400NotAPanic is the reported defect. `panic(err)` meant
// RecoveryMiddleware answered 500, where B3 made every other body path answer 400.
func TestMalformedJsonIsA400NotAPanic(t *testing.T) {
	bodies := map[string]string{
		"truncated":       `{"Username": "ann"`,
		"trailing junk":   `{"Username": "ann"} oops`,
		"not json":        `<xml/>`,
		"top-level array": `[{"Username": "ann"}]`,
		"wrong type":      `{"Username": 42}`,
	}

	controller := controllerWith(nil, &stubStrategy{})

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			message := messageWithBody(body)

			require.NotPanics(t, func() { controller.Login(message) })
			assert.Equal(t, http.StatusBadRequest, message.rec.status)

			decoded := envelope(t, message.rec.body)
			assert.Equal(t, "BAD_REQUEST", decoded["status_code"])
			assert.NotEmpty(t, decoded["errors"])
		})
	}
}

// TestTheBodyIsNeverEchoedBack: a login body that failed to parse is precisely the one
// carrying a password.
func TestTheBodyIsNeverEchoedBack(t *testing.T) {
	const password = "correct-horse-battery-staple"
	message := messageWithBody(`{"Username":"ann","Password":"` + password + `"`)

	controllerWith(nil, &stubStrategy{}).Login(message)

	rendered, err := json.Marshal(message.rec.body)
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), password)
}

// TestAnUnreadableBodyIsA400: the read error used to be discarded, so the handler carried on
// and looked up the user "".
func TestAnUnreadableBodyIsA400(t *testing.T) {
	message := messageWithBody("")
	message.req.readErr = errors.New("connection reset")

	looked := &countingUserService{}
	controller := LoginController{
		userService: looked,
		authContext: &stubAuthContext{strategies: map[string]core.IAuthStrategy{}},
	}

	require.NotPanics(t, func() { controller.Login(message) })
	assert.Equal(t, http.StatusBadRequest, message.rec.status)
	assert.Equal(t, 0, looked.calls, "an unreadable body must not reach a user lookup")
}

type countingUserService struct {
	stubUserService
	calls int
}

func (s *countingUserService) GetByUsername(string) (core.Authenticable, error) {
	s.calls++
	return nil, nil
}

// TestAMissingJwtStrategyIsAnErrorNotAPanic. authContext.Strategy("jwt") returns a nil
// interface when nothing is registered, and the next line called Login on it.
func TestAMissingJwtStrategyIsAnErrorNotAPanic(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "secret")}
	controller := LoginController{
		userService: &stubUserService{user: user},
		authContext: &stubAuthContext{strategies: map[string]core.IAuthStrategy{}},
	}

	message := messageWithBody(`{"Username":"ann","Password":"secret"}`)

	require.NotPanics(t, func() { controller.Login(message) })
	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
	assert.Contains(t, envelope(t, message.rec.body)["errors"], "No `jwt` authentication strategy is registered")
}

// TestAFailingLoginIsAnErrorNotANilDereference. Login reports failure as (nil, err); err was
// assigned and never read, so session.GetId() dereferenced nil on the next line.
func TestAFailingLoginIsAnErrorNotANilDereference(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "secret")}
	strategy := &stubStrategy{session: nil, err: errors.New("token signing failed")}

	message := messageWithBody(`{"Username":"ann","Password":"secret"}`)

	require.NotPanics(t, func() { controllerWith(user, strategy).Login(message) })
	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
}

// TestANilSessionWithoutAnErrorIsAlsoSurvivable — a strategy returning (nil, nil) is out of
// contract, but it is an app's implementation and must not crash the endpoint.
func TestANilSessionWithoutAnErrorIsAlsoSurvivable(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "secret")}
	strategy := &stubStrategy{session: nil, err: nil}

	message := messageWithBody(`{"Username":"ann","Password":"secret"}`)

	require.NotPanics(t, func() { controllerWith(user, strategy).Login(message) })
	assert.Equal(t, http.StatusForbidden, message.rec.status)
}

// TestAnEmptyTokenIsA403NotA200. The Forbidden-coded envelope used to be returned under HTTP
// 200, so a client checking the status code saw success.
func TestAnEmptyTokenIsA403NotA200(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "secret")}
	strategy := &stubStrategy{session: &stubSession{id: ""}}

	message := messageWithBody(`{"Username":"ann","Password":"secret"}`)
	controllerWith(user, strategy).Login(message)

	assert.Equal(t, http.StatusForbidden, message.rec.status,
		"a Forbidden envelope must not be served with a 200")
	assert.Equal(t, "FORBIDDEN", envelope(t, message.rec.body)["status_code"])
}

// TestSuccessReturnsTheToken keeps the happy path honest.
func TestSuccessReturnsTheToken(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "secret")}
	strategy := &stubStrategy{session: &stubSession{id: "the-token"}}

	message := messageWithBody(`{"Username":"ann","Password":"secret"}`)
	controllerWith(user, strategy).Login(message)

	require.Equal(t, http.StatusOK, message.rec.status)

	decoded := envelope(t, message.rec.body)
	body, ok := decoded["body"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "the-token", body["access_token"])
}

// TestBothFieldSpellingsStillWork is why this endpoint keeps encoding/json rather than moving
// to the framework's DTO parsing: encoding/json matches a key case-insensitively, so both
// spellings have always been accepted. The framework's parser looks the key up exactly, so
// switching would silently drop one.
func TestBothFieldSpellingsStillWork(t *testing.T) {
	for _, body := range []string{
		`{"Username":"ann","Password":"secret"}`,
		`{"username":"ann","password":"secret"}`,
	} {
		user := &stubUser{username: "ann", password: hashed(t, "secret")}
		strategy := &stubStrategy{session: &stubSession{id: "tok"}}

		message := messageWithBody(body)
		controllerWith(user, strategy).Login(message)

		assert.Equalf(t, http.StatusOK, message.rec.status, "body %s", body)
	}
}

// TestWrongCredentialsAre401, and the message is in `errors` rather than `body` — which is
// where every other framework error response puts it.
func TestWrongCredentialsAre401(t *testing.T) {
	user := &stubUser{username: "ann", password: hashed(t, "the-real-one")}
	strategy := &stubStrategy{session: &stubSession{id: "tok"}}

	message := messageWithBody(`{"Username":"ann","Password":"guess"}`)
	controllerWith(user, strategy).Login(message)

	require.Equal(t, http.StatusUnauthorized, message.rec.status)

	decoded := envelope(t, message.rec.body)
	assert.Nil(t, decoded["body"], "the message belongs in errors, not body")
	assert.Contains(t, decoded["errors"], "Unauthorized")
}

// TestAnUnknownUserIsAlso401, with the same response — no distinction a caller could use to
// enumerate accounts from the body or the status.
func TestAnUnknownUserIsAlso401(t *testing.T) {
	unknown := messageWithBody(`{"Username":"nobody","Password":"guess"}`)
	controllerWith(nil, &stubStrategy{}).Login(unknown)

	wrongPassword := messageWithBody(`{"Username":"ann","Password":"guess"}`)
	controllerWith(&stubUser{username: "ann", password: hashed(t, "real")},
		&stubStrategy{}).Login(wrongPassword)

	assert.Equal(t, unknown.rec.status, wrongPassword.rec.status)
	assert.Equal(t, envelope(t, unknown.rec.body), envelope(t, wrongPassword.rec.body),
		"an unknown user and a wrong password must be indistinguishable in the response")
}
