package http

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	grglog "github.com/osbits/gorgany/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F2: InputBodyParseError.Error() used to include the raw body, and
// processBodyParsingError opens by logging the error — so every malformed body was
// written verbatim to the log at Error level, and from there to `docker logs` and any
// aggregator. A truncated POST to a login route put a cleartext password there.
//
// New exposure, not pre-existing: before InputBodyParseError was constructed at all, the
// parse path produced an empty ValidationErrors and logged nothing.

// ------------------------------------------------------------------ log capture

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) record(format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, v...))
}

func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = nil
}

type capturingLogger struct{ c *logCapture }

func (l capturingLogger) SetPrefix(string)          {}
func (l capturingLogger) Info(v ...any)             { l.c.record("%v", v) }
func (l capturingLogger) Infof(f string, v ...any)  { l.c.record(f, v...) }
func (l capturingLogger) Warn(v ...any)             { l.c.record("%v", v) }
func (l capturingLogger) Warnf(f string, v ...any)  { l.c.record(f, v...) }
func (l capturingLogger) Error(v ...any)            { l.c.record("%v", v) }
func (l capturingLogger) Errorf(f string, v ...any) { l.c.record(f, v...) }
func (l capturingLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l capturingLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l capturingLogger) Engine() any               { return nil }

var (
	captured    logCapture
	installOnce sync.Once
)

// captureTheLog installs the capturing factory once per test binary — SetLoggerFactory
// panics on a second call — and clears the buffer per test.
func captureTheLog(t *testing.T) *logCapture {
	t.Helper()

	installOnce.Do(func() {
		grglog.SetLoggerFactory(func(string) core.Logger {
			return capturingLogger{c: &captured}
		})
	})

	captured.reset()
	return &captured
}

// ------------------------------------------------------------------ the secret

// secretBody is a body that fails to parse *and* carries a credential — which is the
// realistic case: a client that truncates a login request.
const (
	secretPassword = "correct-horse-battery-staple"
	secretToken    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9-secret"
)

func secretBody(suffix string) string {
	return fmt.Sprintf(`{"username":"ann","password":%q,"token":%q%s`,
		secretPassword, secretToken, suffix)
}

// parseWithCapture runs the JSON parser over body and returns the error and the log.
func parseWithCapture(t *testing.T, body string) (error, string) {
	t.Helper()

	log := captureTheLog(t)

	req, err := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}
	parseErr := parser.Parse(&JSONTestStruct{})

	return parseErr, log.text()
}

// postMessage builds a POST message, since negotiationMessageFor in
// error_negotiation_test.go is GET-only and the method is one of the things the log line
// has to carry.
func postMessage(target string, headers map[string]string) *negotiationMessage {
	raw := httptest.NewRequest(http.MethodPost, target, nil)
	for k, v := range headers {
		raw.Header.Set(k, v)
	}

	rec := &negotiationRecorder{}
	return &negotiationMessage{
		req: &negotiationRequest{raw: raw},
		res: &negotiationResponse{rec: rec},
		rec: rec,
	}
}

// assertNoSecretIn is the assertion the whole file exists for.
func assertNoSecretIn(t *testing.T, where, text string) {
	t.Helper()

	assert.NotContainsf(t, text, secretPassword, "%s leaked the password", where)
	assert.NotContainsf(t, text, secretToken, "%s leaked the token", where)
	assert.NotContainsf(t, text, `"username":"ann"`, "%s leaked body content", where)
}

// ---------------------------------------------------------------------- tests

// TestNoConstructionPathPutsTheBodyInTheErrorString covers all six sites that attach
// string(body), through the parser rather than by constructing the error by hand — so a
// new site cannot be added without this noticing.
func TestNoConstructionPathPutsTheBodyInTheErrorString(t *testing.T) {
	bodies := map[string]string{
		// syntax error: truncated
		"truncated":     secretBody(""),
		"trailing junk": secretBody("} oops"),
		// top-level non-object
		"array":  `[` + secretBody("}") + `]`,
		"string": fmt.Sprintf("%q", secretPassword),
		// over the size limit
		"oversized": secretBody(`,"pad":"` + strings.Repeat("x", maxJSONSize) + `"}`),
		// over the depth limit
		"too deep": strings.Repeat(`{"a":`, maxJSONDepth+1) +
			fmt.Sprintf("%q", secretPassword) + strings.Repeat(`}`, maxJSONDepth+1),
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			err, _ := parseWithCapture(t, body)
			require.Error(t, err)

			assertNoSecretIn(t, "Error()", err.Error())

			// And the body is still reachable for a caller who asks for it.
			var parseError *error2.InputBodyParseError
			if assert.ErrorAs(t, err, &parseError) && parseError.Body != "" {
				assert.Contains(t, parseError.Body, secretPassword,
					"the body must remain on the struct for a deliberate caller")
			}
		})
	}
}

// TestTheHandlerDoesNotLogTheBody is the end of the chain: the framework's own default
// handler, with a real logger installed.
func TestTheHandlerDoesNotLogTheBody(t *testing.T) {
	body := secretBody("")

	err, _ := parseWithCapture(t, body)
	require.Error(t, err)

	log := captureTheLog(t)
	message := negotiationMessageFor("/auth/login", jsonHeaders)
	processBodyParsingError(err, message)

	assertNoSecretIn(t, "the log", log.text())
}

// TestTheHandlerDoesNotPutTheBodyInTheResponse — already true before F2, pinned so it
// stays true.
func TestTheHandlerDoesNotPutTheBodyInTheResponse(t *testing.T) {
	err, _ := parseWithCapture(t, secretBody(""))
	require.Error(t, err)

	for _, headers := range []map[string]string{jsonHeaders, browserHeaders} {
		message := negotiationMessageFor("/auth/login", headers)
		processBodyParsingError(err, message)

		rendered := message.rec.text
		if message.rec.body != nil {
			rendered = fmt.Sprintf("%v", message.rec.body)
		}
		assertNoSecretIn(t, "the response", rendered)
	}
}

// TestTheLogStillIdentifiesTheRequest. Removing the body must not leave an entry saying
// only that *something* sent bad JSON — that is not enough to find the caller.
func TestTheLogStillIdentifiesTheRequest(t *testing.T) {
	err, _ := parseWithCapture(t, secretBody(""))
	require.Error(t, err)

	log := captureTheLog(t)
	processBodyParsingError(err, postMessage("/auth/login", jsonHeaders))

	text := log.text()
	assert.Contains(t, text, "POST", "the log must name the method")
	assert.Contains(t, text, "/auth/login", "the log must name the path")
	assert.Contains(t, text, "unable to parse body", "and why it failed")
}

// TestTheQueryStringIsNotLogged: a query string is caller-supplied and routinely carries a
// token, so requestLine uses URL.Path rather than RequestURI.
func TestTheQueryStringIsNotLogged(t *testing.T) {
	err, _ := parseWithCapture(t, secretBody(""))
	require.Error(t, err)

	log := captureTheLog(t)
	processBodyParsingError(err,
		postMessage("/auth/login?access_token="+secretToken, jsonHeaders))

	assert.NotContains(t, log.text(), secretToken, "the query string must not be logged")
	assert.Contains(t, log.text(), "/auth/login")
}

// TestRequestLineToleratesAMissingRequest: the handlers are reachable from paths where no
// request scope was built.
func TestRequestLineToleratesAMissingRequest(t *testing.T) {
	assert.Equal(t, "<unknown request>", requestLine(nil))

	var err error
	require.NotPanics(t, func() {
		processBodyParsingError(
			error2.NewInputBodyParseError(secretBody(""), "application/json", err),
			negotiationMessageFor("/auth/login", jsonHeaders))
	})
}

// TestErrorStringShape pins the replacement, since it is what a client-facing reason and
// every log line are built from.
func TestErrorStringShape(t *testing.T) {
	err := error2.NewInputBodyParseError(
		secretBody(""), "application/json", fmt.Errorf("unexpected end of JSON input"))

	assert.Equal(t,
		"unable to parse body as application/json: unexpected end of JSON input",
		err.Error())
}
