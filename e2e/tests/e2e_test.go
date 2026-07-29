package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type apiResponse struct {
	Status int               `json:"status"`
	Code   string            `json:"status_code"`
	Body   json.RawMessage   `json:"body"`
	Errors []json.RawMessage `json:"errors"`
}

const (
	fixtureLoginUsername  = "admin"
	fixtureLoginPassword  = "password123"
	fixtureRouteParam     = "fixture-subject"
	fixtureCreateLabel    = "entity-created"
	fixtureCreateText     = "created by dockerized e2e test"
	fixtureUpdateLabel    = "entity-updated"
	fixtureUpdateText     = "updated by dockerized e2e test"
	fixtureRelationTagAID = "tag-red"
	fixtureRelationTagBID = "tag-blue"
)

func TestBootstrapAndRouting(t *testing.T) {
	waitForServer(t)

	resp := mustRequest(t, http.MethodGet, baseURL()+"/health", "", nil, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", resp.StatusCode)
	}

	var health apiResponse
	decodeJSON(t, resp.Body, &health)
	var payload map[string]any
	decodeJSON(t, health.Body, &payload)
	if payload["greeting"] != "hello-from-env" {
		t.Fatalf("expected env-backed greeting, got %v", payload["greeting"])
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/hello/"+fixtureRouteParam, "", nil, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /hello/{name}, got %d", resp.StatusCode)
	}

	var hello apiResponse
	decodeJSON(t, resp.Body, &hello)
	decodeJSON(t, hello.Body, &payload)
	if payload["message"] != "hello "+fixtureRouteParam {
		t.Fatalf("unexpected hello payload: %v", payload)
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/does-not-exist", "", nil, nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got %d", resp.StatusCode)
	}
}

func TestSessionAuthenticationFlow(t *testing.T) {
	waitForServer(t)

	resp := mustRequest(t, http.MethodGet, baseURL()+"/web/protected", "", nil, nil, true)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 for unauthenticated protected route, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "/session/login" {
		t.Fatalf("expected redirect to /session/login, got %q", location)
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/session/login", "", nil, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login page 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "fixture login page") {
		t.Fatalf("unexpected login page body: %s", string(resp.Body))
	}

	form := url.Values{}
	form.Set("username", fixtureLoginUsername)
	form.Set("password", fixtureLoginPassword)
	resp = mustRequest(t, http.MethodPost, baseURL()+"/session/login", "application/x-www-form-urlencoded", []byte(form.Encode()), nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login success 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	sessionCookie := cookieByName(resp.Cookies, "GRG_SESSION_ID")
	if sessionCookie == nil {
		t.Fatal("expected GRG_SESSION_ID cookie to be set")
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/web/protected", "", nil, []*http.Cookie{sessionCookie}, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected authenticated access 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "protected:admin:admin") {
		t.Fatalf("unexpected protected body: %s", string(resp.Body))
	}

	resp = mustRequest(t, http.MethodPost, baseURL()+"/session/logout", "", nil, []*http.Cookie{sessionCookie}, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected logout 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/web/protected", "", nil, []*http.Cookie{sessionCookie}, true)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected protected route to reject old session after logout, got %d", resp.StatusCode)
	}
}

func TestJWTAndWidgetFlow(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/login", map[string]any{
		"username": fixtureLoginUsername,
		"password": fixtureLoginPassword,
	}, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected api login 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var login apiResponse
	decodeJSON(t, resp.Body, &login)
	var tokenPayload map[string]string
	decodeJSON(t, login.Body, &tokenPayload)
	token := tokenPayload["access_token"]
	if token == "" {
		t.Fatal("expected access token in login response")
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/api/v1/protected", "", nil, nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d", resp.StatusCode)
	}

	authHeader := map[string]string{"Authorization": "Bearer " + token}
	resp = mustRequest(t, http.MethodGet, baseURL()+"/api/v1/protected", "", nil, nil, false, authHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected protected api 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/api/v1/widgets", "", nil, nil, false, authHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget list 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var listResp apiResponse
	decodeJSON(t, resp.Body, &listResp)
	var widgets []map[string]any
	decodeJSON(t, listResp.Body, &widgets)
	if len(widgets) == 0 {
		t.Fatal("expected seeded widgets in list response")
	}

	resp = mustJSON(t, http.MethodPost, baseURL()+"/api/v1/widgets", map[string]any{
		"name":        fixtureCreateLabel,
		"description": fixtureCreateText,
	}, authHeader, false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected widget create 201, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var createResp apiResponse
	decodeJSON(t, resp.Body, &createResp)
	var created map[string]any
	decodeJSON(t, createResp.Body, &created)
	createdID, _ := created["id"].(string)
	if createdID == "" {
		t.Fatalf("expected created widget id, body=%v", created)
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/api/v1/widgets/"+createdID, "", nil, nil, false, authHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget fetch 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	resp = mustJSON(t, http.MethodPut, baseURL()+"/api/v1/widgets/"+createdID, map[string]any{
		"name":        fixtureUpdateLabel,
		"description": fixtureUpdateText,
	}, authHeader, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget update 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	resp = mustJSON(t, http.MethodPut, baseURL()+"/api/v1/widgets/"+createdID+"/tags", map[string]any{
		"tagIds": []string{fixtureRelationTagAID, fixtureRelationTagBID},
	}, authHeader, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget tag update 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	db := openDB(t)
	defer db.Close()
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"'", 2)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagAID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagBID+"'", 1)

	resp = mustJSON(t, http.MethodPut, baseURL()+"/api/v1/widgets/"+createdID+"/tags", map[string]any{
		"tagIds": []string{fixtureRelationTagBID},
	}, authHeader, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget tag replacement 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var tagsResp apiResponse
	decodeJSON(t, resp.Body, &tagsResp)
	var updatedWithTags map[string]any
	decodeJSON(t, tagsResp.Body, &updatedWithTags)
	tagObjects, _ := updatedWithTags["tags"].([]any)
	if len(tagObjects) != 1 {
		t.Fatalf("expected exactly one tag after relation cleanup, got %v", updatedWithTags["tags"])
	}

	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagBID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagAID+"'", 0)
}

func TestManyToManyDetachedExistingRelationDoesNotDuplicateInsert(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/login", map[string]any{
		"username": fixtureLoginUsername,
		"password": fixtureLoginPassword,
	}, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected api login 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var login apiResponse
	decodeJSON(t, resp.Body, &login)
	var tokenPayload map[string]string
	decodeJSON(t, login.Body, &tokenPayload)
	token := tokenPayload["access_token"]
	if token == "" {
		t.Fatal("expected access token in login response")
	}

	authHeader := map[string]string{"Authorization": "Bearer " + token}

	resp = mustJSON(t, http.MethodPost, baseURL()+"/api/v1/widgets", map[string]any{
		"name":        "detached-tag-widget",
		"description": "created for detached relation test",
	}, authHeader, false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected widget create 201, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var createResp apiResponse
	decodeJSON(t, resp.Body, &createResp)
	var created map[string]any
	decodeJSON(t, createResp.Body, &created)
	createdID, _ := created["id"].(string)
	if createdID == "" {
		t.Fatalf("expected created widget id, body=%v", created)
	}

	resp = mustJSON(t, http.MethodPut, baseURL()+"/api/v1/widgets/"+createdID+"/tags/detached", map[string]any{
		"tagIds": []string{fixtureRelationTagAID, fixtureRelationTagBID},
	}, authHeader, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected detached widget tag update 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var tagsResp apiResponse
	decodeJSON(t, resp.Body, &tagsResp)
	var updated map[string]any
	decodeJSON(t, tagsResp.Body, &updated)
	tagObjects, _ := updated["tags"].([]any)
	if len(tagObjects) != 2 {
		t.Fatalf("expected exactly two tags after detached relation update, got %v", updated["tags"])
	}

	db := openDB(t)
	defer db.Close()
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_tags WHERE id = '"+fixtureRelationTagAID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_tags WHERE id = '"+fixtureRelationTagBID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"'", 2)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagAID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagBID+"'", 1)
}

func TestUpdatingOneRelationDoesNotClearUnloadedRelations(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/login", map[string]any{
		"username": fixtureLoginUsername,
		"password": fixtureLoginPassword,
	}, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected api login 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var login apiResponse
	decodeJSON(t, resp.Body, &login)
	var tokenPayload map[string]string
	decodeJSON(t, login.Body, &tokenPayload)
	token := tokenPayload["access_token"]
	if token == "" {
		t.Fatal("expected access token in login response")
	}

	authHeader := map[string]string{"Authorization": "Bearer " + token}

	resp = mustJSON(t, http.MethodPost, baseURL()+"/api/v1/widgets", map[string]any{
		"name":        "relation-isolation-widget",
		"description": "created for unloaded relation isolation test",
	}, authHeader, false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected widget create 201, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var createResp apiResponse
	decodeJSON(t, resp.Body, &createResp)
	var created map[string]any
	decodeJSON(t, createResp.Body, &created)
	createdID, _ := created["id"].(string)
	if createdID == "" {
		t.Fatalf("expected created widget id, body=%v", created)
	}

	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(
		"INSERT INTO fixture_widget_shadow_tags (widget_id, tag_id) VALUES ($1, $2)",
		createdID,
		"tag-green",
	); err != nil {
		t.Fatalf("failed to seed shadow relation: %v", err)
	}
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_shadow_tags WHERE widget_id = '"+createdID+"'", 1)

	resp = mustJSON(t, http.MethodPut, baseURL()+"/api/v1/widgets/"+createdID+"/tags", map[string]any{
		"tagIds": []string{fixtureRelationTagAID},
	}, authHeader, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected widget tag update 200, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_tags WHERE widget_id = '"+createdID+"' AND tag_id = '"+fixtureRelationTagAID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_shadow_tags WHERE widget_id = '"+createdID+"'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_widget_shadow_tags WHERE widget_id = '"+createdID+"' AND tag_id = 'tag-green'", 1)
}

func TestValidationAndMultipartParsing(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/parse/json", map[string]any{
		"name":  "parser",
		"count": "bad-type",
	}, nil, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid json payload, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	resp = mustRequest(t, http.MethodGet, baseURL()+"/api/v1/parse/query?search=test&limit=bad", "", nil, nil, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid query payload, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	body, contentType := multipartBody(t, map[string]string{}, "file", "payload.txt", []byte("fixture upload"))
	resp = mustRequest(t, http.MethodPost, baseURL()+"/api/v1/parse/upload", contentType, body, nil, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid multipart payload, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	body, contentType = multipartBody(t, map[string]string{"title": "fixture upload"}, "file", "payload.txt", []byte("fixture upload"))
	resp = mustRequest(t, http.MethodPost, baseURL()+"/api/v1/parse/upload", contentType, body, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid multipart payload, got %d body=%s", resp.StatusCode, string(resp.Body))
	}

	var uploadResp apiResponse
	decodeJSON(t, resp.Body, &uploadResp)
	var uploadBody map[string]any
	decodeJSON(t, uploadResp.Body, &uploadBody)
	fileName, _ := uploadBody["file"].(string)
	if !strings.HasSuffix(fileName, "payload.txt") {
		t.Fatalf("unexpected upload body: %v", uploadBody)
	}
}

// TestMalformedBodiesGet400 is the B3 requirement, end to end.
//
// err.NewInputBodyParseError had zero call sites while both http/error.go and this
// fixture app registered handlers for it, so a malformed body took the *validation*
// path instead — and because that path built an empty ValidationErrors, the framework
// answered a syntax error with a 301 redirect to the Referer. A parse failure is 400,
// distinct from a 422 validation failure, and these are the shapes that separate them.
func TestMalformedBodiesGet400(t *testing.T) {
	waitForServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"truncated object", `{"name": "parser"`},
		{"trailing garbage", `{"name": "parser"} oops`},
		{"top-level array", `[{"name": "parser"}]`},
		{"top-level string", `"just a string"`},
		{"top-level number", `42`},
		{"not json at all", `<xml/>`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := mustRequest(t, http.MethodPost, baseURL()+"/api/v1/parse/json",
				"application/json", []byte(tc.body), nil, true)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d body=%s",
					tc.name, resp.StatusCode, preview(resp.Body))
			}

			// The response must not echo the body back: a body that failed to parse is
			// exactly the kind that might carry a secret halfway through.
			if strings.Contains(string(resp.Body), tc.body) {
				t.Fatalf("the 400 response echoed the request body: %s", preview(resp.Body))
			}

			var parsed apiResponse
			decodeJSON(t, resp.Body, &parsed)
			if parsed.Status != http.StatusBadRequest {
				t.Fatalf("expected the standard envelope with status 400, got %d body=%s",
					parsed.Status, preview(resp.Body))
			}
		})
	}
}

// TestAWellFormedBodyStillParses guards against the 400 path swallowing valid input.
func TestAWellFormedBodyStillParses(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/parse/json", map[string]any{
		"name":  "parser",
		"count": 3,
	}, nil, false)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for a valid payload, got %d body=%s", resp.StatusCode, preview(resp.Body))
	}
}

// TestAValidationFailureIsStill422 pins the other side of the split: a body that parses
// but holds a value a field rejects is 422, not 400.
func TestAValidationFailureIsStill422(t *testing.T) {
	waitForServer(t)

	resp := mustJSON(t, http.MethodPost, baseURL()+"/api/v1/parse/json", map[string]any{
		"name":  "parser",
		"count": "bad-type",
	}, nil, false)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", resp.StatusCode, preview(resp.Body))
	}
}

func TestCliMigrationsAndSeedState(t *testing.T) {
	waitForServer(t)

	db := openDB(t)
	defer db.Close()

	assertIntQuery(t, db, "SELECT COUNT(*) FROM migrations WHERE name IN ('create_sessions_table', 'create_fixture_tables')", 2)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM seeders WHERE name = 'seed_fixture_data'", 1)
	assertIntQuery(t, db, "SELECT COUNT(*) FROM fixture_users", 2)

	var widgetCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM fixture_widgets").Scan(&widgetCount); err != nil {
		t.Fatalf("failed to count widgets: %v", err)
	}
	if widgetCount < 1 {
		t.Fatalf("expected at least one seeded widget, got %d", widgetCount)
	}
}

type response struct {
	StatusCode int
	Body       []byte
	Header     http.Header
	Cookies    []*http.Cookie
}

func baseURL() string {
	if value := os.Getenv("E2E_BASE_URL"); value != "" {
		return value
	}
	return "http://app-server:8080"
}

func waitForServer(t *testing.T) {
	t.Helper()

	if os.Getenv("E2E_BASE_URL") == "" {
		t.Skip("skipping docker e2e test outside configured E2E environment")
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL() + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("server at %s did not become ready in time", baseURL())
}

func mustJSON(t *testing.T, method, target string, payload any, headers map[string]string, noRedirect bool) *response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal json payload: %v", err)
	}

	return mustRequest(t, method, target, "application/json", body, nil, noRedirect, headers)
}

func mustRequest(t *testing.T, method, target, contentType string, body []byte, cookies []*http.Cookie, noRedirect bool, extraHeaders ...map[string]string) *response {
	t.Helper()

	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	for _, headerSet := range extraHeaders {
		for key, value := range headerSet {
			req.Header.Set(key, value)
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if noRedirect {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	t.Logf(
		"http %s %s -> %d cookies=%d req_body=%q resp_body=%q",
		method,
		target,
		resp.StatusCode,
		len(resp.Cookies()),
		preview(body),
		preview(respBody),
	)

	return &response{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Header:     resp.Header.Clone(),
		Cookies:    resp.Cookies(),
	}
}

func decodeJSON(t *testing.T, data []byte, dest any) {
	t.Helper()

	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatalf("failed to decode json: %v body=%s", err, string(data))
	}
}

func multipartBody(t *testing.T, fields map[string]string, fileField, fileName string, content []byte) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("failed to write field %s: %v", key, err)
		}
	}

	part, err := writer.CreateFormFile(fileField, fileName)
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	return buf.Bytes(), writer.FormDataContentType()
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("E2E_DB_DSN")
	if dsn == "" {
		dsn = "postgres://gorgany:gorgany@postgres:5432/gorgany_e2e?sslmode=disable"
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("failed to ping db: %v", err)
	}

	return db
}

func assertIntQuery(t *testing.T, db *sql.DB, query string, expected int) {
	t.Helper()

	var actual int
	if err := db.QueryRow(query).Scan(&actual); err != nil {
		t.Fatalf("query failed %q: %v", query, err)
	}
	t.Logf("sql %q -> got=%d want=%d", query, actual, expected)
	if actual != expected {
		t.Fatalf("unexpected result for %q: got %d want %d", query, actual, expected)
	}
}

func preview(data []byte) string {
	const maxLen = 200
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen]) + "...(truncated)"
}
