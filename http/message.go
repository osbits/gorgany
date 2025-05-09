// http/http_helpers.go
package http

import (
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
	"time"

	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/google/uuid"

	"github.com/go-chi/chi"
	"github.com/spf13/viper"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/decoder"
	"git.qix.sx/gorgany/gorgany.git/model"
)

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

// ----------------- HTTPRequestScope -----------------

// HTTPRequestScope wraps *http.Request and adds query-parsing & secure file upload.
type HTTPRequestScope struct {
	R            *http.Request
	cachedQuery  *decoder.QueryParams
	maxFileBytes int64
	maxBodyBytes int64
	allowedMimes []string
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

func (s *HTTPRequestScope) Body() ([]byte, error) {
	defer s.R.Body.Close()
	limited := io.LimitReader(s.R.Body, s.maxBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxBodyBytes {
		return nil, fmt.Errorf("body too large: %d bytes (max %d)", len(data), s.maxFileBytes)
	}
	return data, nil
}

func (s *HTTPRequestScope) RawRequest() *http.Request {
	return s.R
}

// FormFile enforces size and mime checks, and sanitizes filename.
func (s *HTTPRequestScope) FormFile(key string) (core.IFile, error) {
	if err := s.R.ParseMultipartForm(s.maxFileBytes); err != nil {
		return nil, err
	}
	f, fh, err := s.R.FormFile(key)
	if err != nil {
		return nil, err
	}
	if fh.Size > s.maxFileBytes {
		return nil, fmt.Errorf("file too large: %d > %d", fh.Size, s.maxFileBytes)
	}
	ct := fh.Header.Get("Content-Type")
	if !s.isAllowedMime(ct) {
		return nil, fmt.Errorf("mime %s not allowed", ct)
	}
	name := sanitize(fh.Filename)
	if name == "" {
		return nil, fmt.Errorf("invalid filename")
	}
	mf, err := model.NewMultipartFile(name, f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return mf, nil
}

// GetFiles same checks for multiple.
func (s *HTTPRequestScope) GetFiles(key string) ([]core.IFile, error) {
	if err := s.R.ParseMultipartForm(s.maxFileBytes); err != nil {
		return nil, err
	}
	var out []core.IFile
	for _, fh := range s.R.MultipartForm.File[key] {
		if fh.Size > s.maxFileBytes {
			return nil, fmt.Errorf("file too large: %d", fh.Size)
		}
		ct := fh.Header.Get("Content-Type")
		if !s.isAllowedMime(ct) {
			return nil, fmt.Errorf("mime %s not allowed", ct)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		name := sanitize(fh.Filename)
		mf, err := model.NewMultipartFile(name, f)
		if err != nil {
			f.Close()
			return nil, err
		}
		out = append(out, mf)
	}
	return out, nil
}

func (s *HTTPRequestScope) GetMultipartFormValues() *multipart.Form {
	if err := s.R.ParseMultipartForm(s.maxBodyBytes); err != nil {
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
}

func NewHTTPViewScope(engineRendere core.IEngineRenderer) *HTTPViewScope { return &HTTPViewScope{} }

func (s *HTTPViewScope) Render(ctx context.Context, w io.Writer, tpl string, data map[string]any) {
	if err := s.Engine.DoRender(ctx, w, tpl, data); err != nil {
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

func (s *HTTPSessionScope) Setup() {
	if s.inited {
		return
	}
	s.inited = true

	if msgCtx, ok := s.Ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
		if msgCtx.GetRequest().Method == http.MethodOptions {
			return
		}
	}

	strat := s.Auth.ResolveAuthStrategyByContext(s.Ctx)
	if strat == nil {
		return
	}

	session := strat.CurrentSession(s.Ctx)
	now := time.Now()

	if session != nil && !session.IsExpired() {
		session.SetExpiry(now.Add(s.Storage.GetSessionLifetime() * time.Second))
		s.markFlashUsed(session)
		session.SetLastActivity(time.Now())
		s.Storage.AddSession(session)
		s.current = session
		return
	}

	if session != nil && session.IsExpired() {
		s.Storage.DeleteSession(session)
	}

	newSess, err := strat.NewSessionWithoutUser(s.Ctx)
	if err != nil {
		err2.HandleError(err)
		return
	}
	s.Storage.AddSession(newSess)
	s.current = newSess
}

func (s *HTTPSessionScope) Get() core.ISession {
	if !s.inited {
		s.Setup()
	}
	return s.current
}

func (s *HTTPSessionScope) markFlashUsed(session core.ISession) {
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

	viewEngine     core.IEngineRenderer `container:"inject"`
	authContext    core.IAuthContext    `container:"inject"`
	sessionStorage core.ISessionStorage `container:"inject"`
}

func (m *Message) Init() {
	mCtx := &messageContext{}

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

	msgCtx := context.WithValue(parentRequestCtx, core.MessageContextKey, mCtx)
	m.ctx = context.WithValue(msgCtx, core.DbSessionContextKey, db.Connection().WithContext(msgCtx)) // todo: Currently it can be only GORM Postgres DB

	m.Ses = NewHTTPSessionScope(m.ctx, m.authContext, m.sessionStorage)
	mCtx.session = m.Ses.Get()

	m.Vw = NewHTTPViewScope(m.viewEngine)
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
	return m.ctx
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

				oneTimeParams.Values[key] = append(oneTimeParams.Values[key].([]any), val)
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
		m.Session().Get().SetItem(core.OneTimeSessionAttributeKey, string(buf))
	}

	m.Response().Redirect(urlStr, code)
}

func (s *Message) Close() error {
	if s.request.Body != nil {
		s.request.Body.Close()
	}

	if s.request.MultipartForm != nil {
		for _, fhs := range s.request.MultipartForm.File {
			for _, fh := range fhs {
				if file, err := fh.Open(); err == nil {
					file.Close()
				}
			}
		}
	}

	s.Ses.ClearExpiredFlash()

	return nil
}
