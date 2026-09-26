//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// envelope is the framework's response wrapper.
type envelope struct {
	Status     int             `json:"status"`
	StatusCode string          `json:"status_code"`
	Body       json.RawMessage `json:"body"`
	Errors     []any           `json:"errors"`
}

// reply is what a test asserts on. The body is already read and closed.
type reply struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    []byte
}

// call sends one request and logs enough to debug a failure from the CI log alone.
// header may be nil.
func call(t *testing.T, method, url string, body any, header http.Header) reply {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	t.Logf("%s %s -> %d %s", method, url, resp.StatusCode, truncate(raw))
	return reply{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies(), body: raw}
}

// browser is a client that has done what docs/CSRF.md asks of one: GET /csrf, then
// send the session cookie and the token on every mutating request. The cookie is
// carried by hand: it is a __Host- cookie, which a cookie jar will not send over
// plain HTTP.
type browser struct {
	cookie string
	token  string
}

func newBrowser(t *testing.T, base string) browser {
	t.Helper()
	r := call(t, http.MethodGet, base+"/csrf", nil, nil)
	if r.status != http.StatusOK || len(r.cookies) == 0 {
		t.Fatalf("GET /csrf = %d with %d cookies", r.status, len(r.cookies))
	}
	var token struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(decode(t, r.body).Body, &token); err != nil || token.Token == "" {
		t.Fatalf("GET /csrf returned no token: %v", err)
	}
	// The last Set-Cookie wins, as it does in a browser after a session rotation.
	last := r.cookies[len(r.cookies)-1]
	return browser{cookie: last.Name + "=" + last.Value, token: token.Token}
}

func (b browser) header() http.Header {
	return http.Header{"Cookie": {b.cookie}, "X-Csrf-Token": {b.token}}
}

func decode(t *testing.T, raw []byte) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("response is not the standard envelope: %v\n%s", err, raw)
	}
	return env
}

// database opens the app's database directly, for assertions the API cannot make.
func database(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("E2E_DB_DSN")
	if dsn == "" {
		t.Fatal("E2E_DB_DSN is unset")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
