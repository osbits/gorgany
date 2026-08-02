// http/http_helpers.go
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/util"

	"github.com/go-chi/chi"
	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/decoder"
	"github.com/osbits/gorgany/v2/model"
)

// ResponseWriterWrapper adapts the http.ResponseWriter the server handed us so the framework
// can observe the status code and the last body written.
//
// The four optional interfaces are embedded as fields rather than implemented, and the router
// fills in only the ones the underlying writer actually satisfies (see http/router/gorgany.go).
// That is deliberate — but embedding puts the promoted method in this type's method set
// whether or not the field behind it is nil, so `wrapper.(io.ReaderFrom)` succeeds against a
// writer that has no ReadFrom and the call then dereferences nil. Nothing in the framework
// triggered it while every response went through Write, which is why it sat here unnoticed;
// http.ServeContent does, because io.Copy prefers a destination's ReadFrom, and so does any
// websocket or reverse-proxy handler that reaches for Hijack.
//
// Each of the four is therefore shadowed by an explicit method that checks the field first.
// They stay in the method set — that part is load-bearing, since a writer that *can* stream or
// hijack should still be reachable through the wrapper — but the fallback is now defined
// instead of fatal.
type ResponseWriterWrapper struct {
	http.Flusher
	http.Hijacker
	io.ReaderFrom
	http.ResponseWriter
	io.StringWriter
	io.Writer

	StatusCode int
	Body       io.ReadCloser
	Headers    http.Header
}

func (thiz *ResponseWriterWrapper) WriteHeader(code int) {
	thiz.StatusCode = code
	thiz.ResponseWriter.WriteHeader(code)
}

func (thiz *ResponseWriterWrapper) Header() http.Header {
	thiz.Headers = thiz.ResponseWriter.Header()
	return thiz.ResponseWriter.Header()
}

func (thiz *ResponseWriterWrapper) Write(b []byte) (int, error) {
	thiz.Body = io.NopCloser(bytes.NewBuffer(b))
	return thiz.ResponseWriter.Write(b)
}

// writeOnly hides everything but Write from io.Copy.
//
// Copying with io.Copy(wrapper, r) would find the wrapper's own ReadFrom and call it, which is
// the function doing the copying — so the fallback has to hand io.Copy something that offers
// no shortcut back. Wrapping is how the standard library expresses this too; the alternative
// is a hand-rolled buffer loop that duplicates what io.Copy already does correctly.
type writeOnly struct{ w io.Writer }

func (o writeOnly) Write(p []byte) (int, error) { return o.w.Write(p) }

// ReadFrom streams r into the response, using the underlying writer's own ReadFrom when it has
// one and copying through Write when it does not.
//
// The fallback goes through thiz.Write rather than straight to the underlying writer so there
// is one write path and the wrapper's bookkeeping applies to a streamed response as it does to
// a buffered one. The consequence is that Body ends up holding the final chunk rather than the
// whole payload — which is already what Write does for the last of several writes, and nothing
// in the framework reads Body.
func (thiz *ResponseWriterWrapper) ReadFrom(r io.Reader) (int64, error) {
	if thiz.ReaderFrom != nil {
		return thiz.ReaderFrom.ReadFrom(r)
	}
	return io.Copy(writeOnly{thiz}, r)
}

// WriteString writes s, using the underlying writer's own WriteString when it has one.
func (thiz *ResponseWriterWrapper) WriteString(s string) (int, error) {
	if thiz.StringWriter != nil {
		return thiz.StringWriter.WriteString(s)
	}
	return thiz.Write([]byte(s))
}

// Flush flushes buffered output when the underlying writer can, and does nothing when it
// cannot. A writer with no Flusher has nothing buffered to lose, so a silent no-op is the
// truthful answer rather than a swallowed failure.
func (thiz *ResponseWriterWrapper) Flush() {
	if thiz.Flusher != nil {
		thiz.Flusher.Flush()
	}
}

// Hijack takes over the connection when the underlying writer supports it, and reports that it
// does not when it cannot. http.ErrNotSupported is what callers already handle — net/http
// returns it from its own non-hijackable writers — so a caller that checks gets the answer it
// expects instead of a panic RecoveryMiddleware turns into a 500.
func (thiz *ResponseWriterWrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if thiz.Hijacker != nil {
		return thiz.Hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ----------------- HTTPRequestScope -----------------

// multipartFramingAllowance is the slack the raw-body cap gets over the total upload
// budget while a multipart form is being parsed. See app.MaxRequestBodyBytes: the framing
// of a multipart body counts against a reader limit but not against the upload budget.
const multipartFramingAllowance int64 = 1 * 1024 * 1024

// HTTPRequestScope wraps *http.Request and adds query-parsing & secure file upload.
type HTTPRequestScope struct {
	R            *http.Request
	cachedQuery  *decoder.QueryParams
	maxFileBytes int64
	maxBodyBytes int64
	allowedMimes []string
	bodyCapped   bool
	// stored are the temp copies FormFile and GetFiles wrote, held until the request ends.
	// See storeUpload.
	stored []io.Closer
}

// NewHTTPRequestScope reads file-size and mimes from viper (in MB / slice).
func NewHTTPRequestScope(r *http.Request) *HTTPRequestScope {
	maxFileSizeMB := viper.GetInt64("http.security.file.maxSizeMB")
	if maxFileSizeMB <= 0 {
		maxFileSizeMB = 100 // default 100 MB
	}

	mimes := viper.GetStringSlice("http.security.file.allowedMimes")
	if len(mimes) == 0 {
		mimes = []string{"image/jpeg", "image/png", "application/pdf", "text/plain", "application/octet-stream"}
	}

	maxBodySize := viper.GetInt64("http.security.body.maxSizeMB")
	if maxBodySize <= 0 {
		maxBodySize = 10 // default 10 MB
	}

	return &HTTPRequestScope{
		R:            r,
		maxFileBytes: maxFileSizeMB << 20,
		maxBodyBytes: maxBodySize << 20,
		allowedMimes: mimes,
	}
}

func (s *HTTPRequestScope) Locale() string {
	if lang := s.PathParam("lang"); lang != "" {
		return lang
	}
	return viper.GetString("i18n.lang.default")
}

// PathParam returns chi URL-param.
func (s *HTTPRequestScope) PathParam(name string) string {
	return chi.URLParam(s.R, name)
}

// QueryParam returns single value after decoder parsing.
func (s *HTTPRequestScope) QueryParam(key string) string {
	if err := s.parseQuery(); err != nil {
		return ""
	}
	return s.cachedQuery.GetString(key)
}

// QueryParams returns []string for key.
func (s *HTTPRequestScope) QueryParams(key string) []string {
	if err := s.parseQuery(); err != nil {
		return nil
	}
	return s.cachedQuery.GetArray(key)
}

// QueryParamsMap returns []map[string]string for key.
func (s *HTTPRequestScope) QueryParamsMap(key string) []map[string]string {
	if err := s.parseQuery(); err != nil {
		return nil
	}
	return s.cachedQuery.GetArrayMap(key)
}

// RawQuery returns r.URL.RawQuery.
func (s *HTTPRequestScope) RawQuery() string {
	return s.R.URL.RawQuery
}

func (s *HTTPRequestScope) Query() core.QueryParams {
	if err := s.parseQuery(); err != nil {
		return nil
	}
	return s.cachedQuery
}

// Header gives direct access.
func (s *HTTPRequestScope) Header() http.Header {
	return s.R.Header
}

// BodyReader returns the raw Body. Caller must Close().
func (s *HTTPRequestScope) BodyReader() io.ReadCloser {
	return s.R.Body
}

func (s *HTTPRequestScope) Body() (body []byte, err error) {
	defer func() {
		s.R.Body.Close()
		s.R.Body = io.NopCloser(bytes.NewBuffer(body))
	}()

	limited := io.LimitReader(s.R.Body, s.maxBodyBytes+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > s.maxBodyBytes {
		// The limit named here is the one that was applied. It used to report maxFileBytes,
		// a different and much larger number, so the message told an operator the body was
		// under the limit it had just been rejected for.
		return nil, fmt.Errorf("body too large: %d bytes (max %d)", len(body), s.maxBodyBytes)
	}
	return body, nil
}

func (s *HTTPRequestScope) RawRequest() *http.Request {
	return s.R
}

// CapBody holds the request body to at most limit bytes, wherever it is read from.
//
// The point is that the reader refuses at the limit instead of after it: MaxBytesReader
// only ever pulls limit+1 bytes out of the connection, so an oversized body is never
// buffered, never spilled to a temp file, and never counted after the fact.
//
// Applied at most once per scope. Message.Init has already wrapped the body in the outer
// ceiling by the time anything calls this, so a capped body is normally two wrappers deep
// and the effective limit is the smaller of the two; what the guard prevents is a third
// from a second parse, because each wrapper carries its own budget and re-wrapping a
// partly-read body makes the effective limit impossible to reason about.
func (s *HTTPRequestScope) CapBody(limit int64) {
	if s.bodyCapped || s.R.Body == nil || limit <= 0 {
		return
	}
	s.R.Body = http.MaxBytesReader(nil, s.R.Body, limit)
	s.bodyCapped = true
}

// parseMultipart caps the body and then parses the form.
//
// The order is the whole fix. GetMultipartFormValues used to parse first and let
// MultipartParser check the sizes afterwards, and the argument it passed is the *in-memory*
// budget rather than a ceiling — so a body above it spilled to the OS temp directory with
// no total limit at all, and DefaultMaxFileSize was advisory: the 10 MB check ran once the
// bytes were on disk. FormFile passed maxFileBytes, which defaults to 100 MB, so a
// concurrent request could hold that much resident as well.
//
// The in-memory budget is now the smaller of the configured body size and the total upload
// budget, and the total budget is enforced by the reader while the parse is still running.
func (s *HTTPRequestScope) parseMultipart() error {
	if s.R.MultipartForm != nil {
		return nil
	}

	limits := resolveUploadLimits()
	s.CapBody(limits.MaxMultipartSize + multipartFramingAllowance)

	inMemory := s.maxBodyBytes
	if limits.MaxMultipartSize < inMemory {
		inMemory = limits.MaxMultipartSize
	}

	return s.R.ParseMultipartForm(inMemory)
}

// FormFile enforces size and mime checks, and sanitizes filename.
func (s *HTTPRequestScope) FormFile(key string) (core.IFile, error) {
	if err := s.parseMultipart(); err != nil {
		return nil, err
	}
	f, fh, err := s.R.FormFile(key)
	if err != nil {
		return nil, err
	}
	if fh.Size > s.maxFileBytes {
		f.Close()
		return nil, fmt.Errorf("file too large: %d > %d", fh.Size, s.maxFileBytes)
	}
	name := sanitize(fh.Filename)
	if name == "" {
		f.Close()
		return nil, fmt.Errorf("invalid filename")
	}
	mf, err := s.storeUpload(name, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	return mf, nil
}

// GetFiles same checks for multiple.
func (s *HTTPRequestScope) GetFiles(key string) ([]core.IFile, error) {
	if err := s.parseMultipart(); err != nil {
		return nil, err
	}
	var out []core.IFile

	// A failure part-way through leaves the files already stored with nobody to release
	// them, so they are closed here rather than left in resource/temp.
	fail := func(err error) ([]core.IFile, error) {
		for _, stored := range out {
			_ = stored.Close()
		}
		return nil, err
	}

	for _, fh := range s.R.MultipartForm.File[key] {
		if fh.Size > s.maxFileBytes {
			return fail(fmt.Errorf("file too large: %d", fh.Size))
		}
		f, err := fh.Open()
		if err != nil {
			return fail(err)
		}
		mf, err := s.storeUpload(sanitize(fh.Filename), f)
		f.Close()
		if err != nil {
			return fail(err)
		}
		out = append(out, mf)
	}
	return out, nil
}

// storeUpload writes one part to temp storage and holds it to the configured mime
// allowlist.
//
// The allowlist used to be applied to fh.Header.Get("Content-Type"), which is a value the
// uploading client writes: declaring `image/png` on a part full of HTML satisfied it. The
// check now runs against what the content actually sniffs as, which model.NewMultipartFile
// has already determined in order to pick the stored extension — so this costs no second
// read of the body, and a part that fails it is deleted again rather than left in
// resource/temp.
func (s *HTTPRequestScope) storeUpload(name string, part io.Reader) (core.IFile, error) {
	mf, err := model.NewMultipartFile(name, part)
	if err != nil {
		return nil, err
	}

	if !s.isAllowedMime(mf.MediaType()) {
		_ = mf.Close()
		return nil, fmt.Errorf("mime %s not allowed", mf.MediaType())
	}

	// The caller is about to publish this, so neither FormFile nor GetFiles can close what
	// it returns — but somebody has to, or resource/temp grows by one file per upload for
	// the life of the deployment. The request-scoped cleanup only ever saw the copies
	// MultipartParser had bound onto a DTO, which left every app taking its uploads through
	// these two methods leaking exactly as before. Message.Close drains this list after the
	// handler has returned, which is the first moment nothing can still want the copy.
	s.stored = append(s.stored, mf)

	return mf, nil
}

// releaseStoredUploads deletes the temp copies FormFile and GetFiles wrote. Called from
// Message.Close; MultipartFile.Close is idempotent and leaves a published upload alone, so
// a handler that closed its own file first is not a problem.
func (s *HTTPRequestScope) releaseStoredUploads() {
	for _, stored := range s.stored {
		if err := stored.Close(); err != nil {
			err2.HandleError(err)
		}
	}
	s.stored = nil
}

// GetMultipartFormValues parses the form, or returns nil when it cannot.
//
// Nil means "malformed, or over a limit". Callers must treat it as a client error: the one
// caller in the framework used to range straight over form.File, so any body that failed
// to parse — and every body over the new ceiling — was a nil-pointer panic on a path an
// unauthenticated client controls.
func (s *HTTPRequestScope) GetMultipartFormValues() *multipart.Form {
	if err := s.parseMultipart(); err != nil {
		return nil
	}
	return s.R.MultipartForm
}

// IP resolves real client IP.
func (s *HTTPRequestScope) IP() string {
	if x := s.R.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	if x := s.R.Header.Get("X-Real-IP"); x != "" {
		return x
	}
	h, _, e := net.SplitHostPort(s.R.RemoteAddr)
	if e != nil {
		return s.R.RemoteAddr
	}
	return h
}

func (s *HTTPRequestScope) parseQuery() error {
	if s.cachedQuery != nil {
		return nil
	}
	vals, err := url.ParseQuery(s.R.URL.RawQuery)
	if err != nil {
		return err
	}
	qp, err := decoder.ParseUrlValues(vals)
	if err != nil {
		return err
	}
	s.cachedQuery = &qp
	return nil
}

func (s *HTTPRequestScope) isAllowedMime(ct string) bool {
	for _, a := range s.allowedMimes {
		if a == ct {
			return true
		}
	}
	return false
}

func sanitize(name string) string {
	base := filepath.Base(name)
	re := regexp.MustCompile(`[^a-zA-Z0-9\-\._]`)
	return re.ReplaceAllString(base, "")
}

// ----------------- HTTPResponseScope -----------------

type HTTPResponseScope struct {
	W http.ResponseWriter
	r *http.Request
}

func NewHTTPResponseScope(w http.ResponseWriter, r *http.Request) *HTTPResponseScope {
	return &HTTPResponseScope{
		W: w,
		r: r,
	}
}
func (s *HTTPResponseScope) SetHeader(k, v string) { s.W.Header().Set(k, v) }
func (s *HTTPResponseScope) Header() http.Header   { return s.W.Header() }
func (s *HTTPResponseScope) Text(b string, c int) {
	s.SetHeader("Content-Type", "text/plain; charset=utf-8")
	s.W.WriteHeader(c)
	s.W.Write([]byte(b))
}
func (s *HTTPResponseScope) JSON(v interface{}, c int) {
	s.SetHeader("Content-Type", "application/json; charset=utf-8")
	s.W.WriteHeader(c)
	json.NewEncoder(s.W).Encode(v)
}
func (s *HTTPResponseScope) Bytes(b []byte, c int) {
	s.W.WriteHeader(c)
	s.W.Write(b)
}
func (s *HTTPResponseScope) Redirect(u string, c int) {
	http.Redirect(s.W, s.r, u, c)
}

func (s *HTTPResponseScope) RawWriter() http.ResponseWriter {
	return s.W
}

// ----------------- HTTPViewScope -----------------

type HTTPViewScope struct {
	Engine core.IEngineRenderer
	Ctx    context.Context
	Writer http.ResponseWriter
}

func NewHTTPViewScope(ctx context.Context, writer http.ResponseWriter, engineRenderer core.IEngineRenderer) *HTTPViewScope {
	return &HTTPViewScope{
		Engine: engineRenderer,
		Ctx:    ctx,
		Writer: writer,
	}
}

func (s *HTTPViewScope) Render(tpl string, data map[string]any) {
	// The rendered template used to go straight to the writer with no Content-Type, leaving
	// net/http's sniffer to guess from the first 512 bytes. That was survivable only because
	// browsers guessed as well; now that every response carries nosniff, the browser honours
	// whatever the sniffer decided — and http.DetectContentType recognises only a fixed list
	// of opening tags, so a fragment beginning <ul>, <span>, <section>, <form> or <tr> is
	// answered text/plain and shown to the visitor as source.
	//
	// Only when nothing has been chosen already: an app rendering a template that is not html
	// — a sitemap, a feed, a plain-text mail body — sets the type before calling this, and
	// that choice has to win.
	if s.Writer.Header().Get("Content-Type") == "" {
		s.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	}

	if err := s.Engine.DoRender(s.Ctx, s.Writer, tpl, data); err != nil {
		panic(err)
	}
}

// ----------------- HTTPSessionScope -----------------

type HTTPSessionScope struct {
	Auth    core.IAuthContext
	Storage core.ISessionStorage
	Ctx     context.Context

	current core.ISession
	inited  bool
}

func NewHTTPSessionScope(ctx context.Context, authContext core.IAuthContext, sessionStorage core.ISessionStorage) *HTTPSessionScope {
	return &HTTPSessionScope{Ctx: ctx, Auth: authContext, Storage: sessionStorage}
}

func (s *HTTPSessionScope) Get() core.ISession {
	if s.current != nil {
		return s.current
	}

	strat := s.Auth.ResolveAuthStrategyByContext(s.Ctx)
	if strat == nil {
		return nil
	}

	s.current = strat.CurrentSession(s.Ctx)
	return s.current
}

func (s *HTTPSessionScope) Set(session core.ISession) {
	s.current = session
}

func (s *HTTPSessionScope) ClearExpiredFlash() {
	session := s.Get()
	if session == nil {
		return
	}

	item := session.GetItem(core.OneTimeSessionAttributeKey)
	if item == "" {
		return
	}
	oneTimeParams := model.OneTimeParams{}
	err := json.Unmarshal([]byte(item), &oneTimeParams)
	if err != nil {
		err2.HandleError(err)
		return
	}

	if oneTimeParams.Start {
		return
	}

	session.ClearItem(core.OneTimeSessionAttributeKey)
}

// PublishSession installs session as the one the rest of this request sees.
//
// A handler that replaces the request's session has to say so, because two places cache it
// for the life of the request. The session scope above answers Get from s.current once it has
// resolved, and the message context holds its own copy which
// StandardAuthStrategy.ResolveSessionId consults *before* looking at the cookie. Both were
// filled in by the session middleware before the handler ran, so leaving them alone means
// everything asked afterwards is answered about a session that no longer exists:
// message.Session().Get() hands back the old object, and IsLoggedIn looks the old identifier
// up in the store, finds nothing, and reports the visitor as anonymous. A login that fully
// succeeded is then indistinguishable from one that failed — which is why this exists at all:
// Login rotates the session identifier, so every successful login replaces the session.
//
// Calling Message.Context() is what copies the scope's session onto the message context, and
// it writes into the context object in place, so a context.Context a handler captured before
// the swap sees the new session as well.
//
// The identity access control memoised for this request is dropped for the same reason. That
// memo is keyed on what the identity was derived from, so a rotated session misses it on its
// own; the explicit drop is here because a session implementation whose identifier or user id
// does not visibly move — a store that reuses the identifier, say — would key the same and be
// answered about the pre-login principal for the rest of the request. A stale identity here is
// an authorization decision made about the wrong user, so it is worth being unconditional
// about on the one path that is known to replace the principal.
//
// The CSRF token header is rewritten for the same reason. SessionMiddleware publishes
// X-CSRF-Token before the handler runs, so on a request whose handler replaces the session the
// header names the session that has gone — and the documented client contract is to re-read
// the token from every response, which would leave a client holding a token its session does
// not have and every mutating request it makes rejected. A login is precisely such a request:
// the new session's token is deliberately not the old one's.
func PublishSession(message core.HttpMessage, session core.ISession) {
	if message == nil || session == nil {
		return
	}

	if editable, ok := message.Session().(core.IEditableSessionScope); ok {
		editable.Set(session)
	}

	// The order matters: the session is on the message context by the time the memo is
	// dropped, so a resolution racing in behind this cannot store the old identity back.
	ctx := message.Context()
	if memo, ok := ctx.Value(core.MessageContextKey).(core.IRequestIdentityMemo); ok {
		memo.InvalidateIdentity()
	}

	// Only when there is one to publish: a session with no token yet is left for whoever mints
	// it, rather than having the header cleared out from under an earlier correct value.
	if token := session.GetItem(core.CSRFSessionKey); token != "" {
		message.Response().Header().Set(core.CSRFTokenHeader, token)
	}
}

// ----------------- Message + Context init -----------------

// Message is a facade for controllers and dispatcher.
type Message struct {
	Req           core.IRequestScope
	Res           core.IResponseScope
	Ses           core.ISessionScope
	Vw            core.IViewScope
	CookieManager core.ICookieManager

	writer  http.ResponseWriter
	request *http.Request

	ctx context.Context

	// closers are resources whose lifetime is the request rather than the call that
	// created them — the temp copies of uploaded files, above all. See RegisterCloser.
	closers []io.Closer

	viewEngine     core.IEngineRenderer `container:"inject"`
	authContext    core.IAuthContext    `container:"inject"`
	sessionStorage core.ISessionStorage `container:"inject"`
}

func (m *Message) Init() {
	mCtx := &messageContext{}

	// The body ceiling is applied here, before any middleware, handler or parser can read
	// a byte of it. Doing it at the server boundary as well is not enough on its own: an
	// app is free to mount this router inside a server it built itself, and every request
	// still goes through exactly one message.
	if m.request.Body != nil {
		m.request.Body = http.MaxBytesReader(m.writer, m.request.Body, app.MaxRequestBodyBytes())
	}

	m.Req = NewHTTPRequestScope(m.request)
	m.Res = NewHTTPResponseScope(m.writer, m.request)

	cookieManager := NewCookieManager(m.writer, m.request)

	mCtx.url = m.Req.RawRequest().URL
	mCtx.requestURI = m.Req.RawRequest().RequestURI
	mCtx.cookieManager = cookieManager
	mCtx.headers = m.Req.Header()
	mCtx.request = m.Req.RawRequest()
	mCtx.requestId = uuid.New().String()
	mCtx.ip = m.Req.IP()

	parentRequestCtx := m.Req.RawRequest().Context()
	mCtx.requestCtx = parentRequestCtx

	m.ctx = context.WithValue(parentRequestCtx, core.MessageContextKey, mCtx)
	//m.ctx = context.WithValue(msgCtx, core.DbSessionContextKey, db.Connection().WithContext(msgCtx)) // todo: Currently it can be only GORM Postgres DB

	m.Ses = NewHTTPSessionScope(m.ctx, m.authContext, m.sessionStorage)

	m.Vw = NewHTTPViewScope(m.ctx, m.writer, m.viewEngine)
	m.CookieManager = cookieManager
}

func (m *Message) Request() core.IRequestScope {
	return m.Req
}

func (m *Message) Response() core.IResponseScope {
	return m.Res
}

func (m *Message) Cookie() core.ICookieManager {
	return m.CookieManager
}

func (m *Message) View() core.IViewScope {
	return m.Vw
}

func (m *Message) Session() core.ISessionScope {
	return m.Ses
}

func (m *Message) Context() context.Context {
	msgCtx := m.ctx.Value(core.MessageContextKey)
	if msgCtx == nil {
		return context.Background()
	}

	if msgCtx, ok := msgCtx.(*messageContext); ok {
		msgCtx.setSession(m.Ses.Get())
		return m.ctx
	}

	return context.Background()
}

// RedirectWithFlash saves flash data and redirects.
func (m *Message) RedirectWithFlash(urlStr string, code int, data map[string]interface{}) {
	oneTimeParams := model.OneTimeParams{
		Values: make(map[string]any),
		Start:  true,
	}

	for key, value := range data {
		rType := util.IndirectType(reflect.TypeOf(value))
		if rType.Kind() == reflect.Slice {
			slice := util.InterfaceSlice(value)
			if oneTimeParams.Values[key] == nil {
				oneTimeParams.Values[key] = make([]any, 0)
			}
			for _, sliceValue := range slice {
				val := value

				str, ok := sliceValue.(fmt.Stringer)
				if ok {
					val = str.String()
				}

				// oneTimeParams.Values is built locally just above and only this loop
				// writes it, so today this assertion cannot fail. It is checked anyway
				// because the surrounding code is a redirect path: a future change that
				// pre-populates the map would otherwise turn into a panic mid-response
				// rather than a dropped flash value.
				existing, ok := oneTimeParams.Values[key].([]any)
				if !ok {
					existing = []any{}
				}
				oneTimeParams.Values[key] = append(existing, val)
			}
		} else {
			val := value

			str, ok := value.(fmt.Stringer)
			if ok {
				val = str.String()
			}

			oneTimeParams.Values[key] = val
		}
	}

	buf, err := json.Marshal(oneTimeParams)
	if err != nil {
		err2.HandleError(err)
	} else {
		if session := m.Session().Get(); session != nil {
			session.SetItem(core.OneTimeSessionAttributeKey, string(buf))
		}
	}

	m.Response().Redirect(urlStr, code)
}

// RegisterCloser hands c to the message to be closed when the request ends.
//
// This exists for the temp copies the upload path writes to resource/temp. Whoever creates
// one cannot close it: the file is bound into the handler's DTO and the handler is expected
// to publish it with Write, which reads from that very temp copy. Closing at the end of the
// binding call would delete the upload out from under the handler — which is what the
// `todo: need to fix the closing (removing) of temp files` in MultipartParser.Parse was
// about, and why the cleanup was left commented out and the files simply accumulated.
//
// The end of the request is the first moment at which nothing can still want them.
func (m *Message) RegisterCloser(c io.Closer) {
	if c == nil {
		return
	}
	m.closers = append(m.closers, c)
}

func (s *Message) Close() error {
	if s.request.Body != nil {
		s.request.Body.Close()
	}

	// Reverse order, so a file registered after another is released first — the same rule
	// as a stack of defers, and the one a caller will assume.
	for i := len(s.closers) - 1; i >= 0; i-- {
		if err := s.closers[i].Close(); err != nil {
			err2.HandleError(err)
		}
	}
	s.closers = nil

	// The uploads the request scope stored on its own account, which no DTO ever bound. Req
	// is declared as the interface, so an app that supplied its own scope is simply skipped
	// — it owns whatever it wrote.
	if scope, ok := s.Req.(*HTTPRequestScope); ok {
		scope.releaseStoredUploads()
	}

	if s.request.MultipartForm != nil {
		// RemoveAll, not a loop that opens each part and closes it again. That loop could
		// not delete anything — it created a *second* handle per part and closed that —
		// so every part the parser spilled past its in-memory budget stayed in the OS temp
		// directory for the life of the process.
		if err := s.request.MultipartForm.RemoveAll(); err != nil {
			err2.HandleError(err)
		}
	}

	s.Ses.ClearExpiredFlash()

	return nil
}
