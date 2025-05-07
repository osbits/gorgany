package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/decoder"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/util"
	"git.qix.sx/gorgany/gorgany.git/view"
	"github.com/go-chi/chi"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	url2 "net/url"
	"reflect"
	"strings"
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

type Message struct {
	writer  http.ResponseWriter
	request *http.Request

	cachedQuery    *decoder.QueryParams
	currentSession core.ISession

	authContext    core.IAuthContext    `container:"inject"`
	sessionStorage core.ISessionStorage `container:"inject"`
	cookieManager  *CookieManager

	ctx context.Context

	inputParameters []reflect.Value

	engineRenderer *view.EngineRenderer `container:"inject"`

	io []io.Closer
}

func (thiz *Message) Init() {
	thiz.io = make([]io.Closer, 0)
	thiz.cookieManager = NewCookieManager(thiz.writer, thiz.request)
}

func (thiz *Message) GetRequest() *http.Request {
	return thiz.request
}

func (thiz *Message) GetWriter() http.ResponseWriter {
	return thiz.writer
}

func (thiz *Message) GetPathParam(key string) string {
	return chi.URLParam(thiz.request, key)
}

// GetBody returns body in bytes
func (thiz *Message) GetBody() []byte {
	bodyCloser := thiz.request.Body
	body, err := io.ReadAll(bodyCloser)
	if err != nil {
		panic(fmt.Errorf("Error during read body from request, %v", err))
	}
	thiz.request.Body.Close()
	thiz.request.Body = io.NopCloser(bytes.NewBuffer(body))

	return body
}

// GetBodyContent returns body in string
func (thiz *Message) GetBodyContent() string {
	body := thiz.GetBody()
	return string(body)
}

func (thiz *Message) GetHeader() http.Header {
	return thiz.request.Header
}

func (thiz *Message) GetCookie(key string) *http.Cookie {
	return thiz.cookieManager.GetCookie(key)
}

func (thiz *Message) Render(template string, options map[string]any) {
	if options == nil {
		options = make(map[string]any)
	}
	oneTimeParams := thiz.OneTimeParams()
	for key, values := range oneTimeParams {
		options[key] = values
	}

	options = thiz.addOptionsToView(options)
	err := thiz.engineRenderer.DoRender(thiz.Context(), thiz.writer, template, options)
	if err != nil {
		panic(fmt.Errorf("Error during render template '%s', %v", template, err))
	}
}

func (thiz *Message) ResponseHeader() http.Header {
	return thiz.writer.Header()
}

func (thiz *Message) Response(responseBody string, statusCode int) {
	thiz.writer.WriteHeader(statusCode)
	_, err := thiz.writer.Write([]byte(responseBody))
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", responseBody, err))
	}
}

func (thiz *Message) ResponseJSON(responseBody any, statusCode int) {
	var respBody string
	switch responseBody.(type) {
	case string:
		respBody = responseBody.(string)
	default:
		respBodyBytes, err := json.Marshal(responseBody)
		if err != nil {
			panic(err)
		}
		respBody = string(respBodyBytes)
	}
	thiz.writer.Header().Set("Content-Type", "application/json")
	thiz.Response(respBody, statusCode)
}

func (thiz *Message) ResponseBytes(responseBody []byte, statusCode int) {
	thiz.writer.WriteHeader(statusCode)
	_, err := thiz.writer.Write(responseBody)
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", string(responseBody), err))
	}
}

func (thiz *Message) SetCookie(cookie *http.Cookie) {
	http.SetCookie(thiz.writer, cookie)
}

func (thiz *Message) RedirectWithParams(url string, redirectCode int, params map[string]any) {
	oneTimeParams := model.OneTimeParams{
		Values: make(map[string]any),
		Start:  true,
	}

	for key, value := range params {
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
		thiz.GetSession().SetItem(core.OneTimeSessionAttributeKey, string(buf))
	}

	url = util.AddLocaleToURL(thiz.Locale(), url)
	http.Redirect(thiz.writer, thiz.request, url, redirectCode)
}

func (thiz *Message) Redirect(url string, redirectCode int) {
	url = util.AddLocaleToURL(thiz.Locale(), url)
	http.Redirect(thiz.writer, thiz.request, url, redirectCode)
}

func (thiz *Message) OneTimeParams() map[string]any {
	session := thiz.GetSession()
	if session == nil {
		return nil
	}

	params := session.GetItem(core.OneTimeSessionAttributeKey)

	if params == "" {
		return nil
	}

	oneTimeParams := model.OneTimeParams{}

	err := json.Unmarshal([]byte(params), &oneTimeParams)
	if err != nil {
		err2.HandleError(err)
		return nil
	}

	return oneTimeParams.Values
}

func (thiz *Message) GetBearerToken() string {
	bearerToken := thiz.GetHeader().Get("Authorization")
	return util.ParseBearerToken(bearerToken)
}

func (thiz *Message) parseQueryParams() error {
	if thiz.cachedQuery != nil {
		return nil
	}

	params, err := url2.ParseQuery(thiz.request.URL.RawQuery)
	if err != nil {
		return err
	}

	processedParams, err := decoder.ParseUrlValues(params)
	if err != nil {
		return err
	}

	thiz.cachedQuery = &processedParams
	return nil
}

func (thiz *Message) GetQueryParam(key string) string {
	err := thiz.parseQueryParams()
	if err != nil {
		return ""
	}
	return thiz.cachedQuery.GetString(key)
}

func (thiz *Message) GetQueryParams(key string) []string {
	err := thiz.parseQueryParams()
	if err != nil {
		return []string{}
	}
	return thiz.cachedQuery.GetArray(key)
}

func (thiz *Message) GetQueryParamsMap(key string) []map[string]string {
	err := thiz.parseQueryParams()
	if err != nil {
		return []map[string]string{}
	}
	return thiz.cachedQuery.GetArrayMap(key)
}

func (thiz Message) GetRawQuery() string {
	return thiz.request.URL.RawQuery
}

func (thiz Message) GetQuery() core.QueryParams {
	err := thiz.parseQueryParams()
	if err != nil {
		err2.HandleError(err)
		return nil
	}

	if thiz.cachedQuery != nil {
		return *thiz.cachedQuery
	}
	return nil
}

func (thiz Message) GetBodyParam(key string) any {
	parsedBody := make(map[string]any)
	contentType := thiz.GetHeader().Get("Content-Type")
	if contentType == "application/json" {
		err := json.Unmarshal(thiz.GetBody(), &parsedBody)
		if err != nil {
			return ""
		}
		return parsedBody[key]
	}
	log.Log("").Warnf("http.Message: GetBodyParam is not implemented for %s yet", contentType)
	return ""
}

func (thiz *Message) GetMultipartFormValues() *multipart.Form {
	err := thiz.request.ParseMultipartForm(10000) //todo
	if err != nil {
		return nil
	}
	return thiz.request.MultipartForm
}

func (thiz *Message) Locale() string {
	lang := chi.URLParam(thiz.request, "lang")
	if lang == "" {
		lang = viper.GetString("i18n.lang.default")
	}
	return lang
}

func (thiz *Message) GetFile(key string) (core.IFile, error) {
	thiz.GetMultipartFormValues()
	fileRequest, header, err := thiz.request.FormFile(key)
	if err != nil {
		if strings.Contains(err.Error(), "no such file") {
			return nil, nil
		}
		return nil, err
	}

	thiz.io = append(thiz.io, fileRequest)

	file, err := model.NewMultipartFile(header.Filename, fileRequest)
	if err != nil {
		return nil, err
	}

	thiz.io = append(thiz.io, file)

	return file, nil
}

func (thiz *Message) GetFiles(key string) ([]core.IFile, error) {
	files := make([]core.IFile, 0)

	multipartForm := thiz.GetMultipartFormValues()
	filesRequest := multipartForm.File
	for mapKey, val := range filesRequest {
		if mapKey != key {
			continue
		}
		for _, f := range val {
			reader, err := f.Open()
			if err != nil {
				return nil, err
			}

			thiz.io = append(thiz.io, reader)

			file, err := model.NewMultipartFile(f.Filename, reader)

			thiz.io = append(thiz.io, file)

			files = append(files, file)
		}
	}
	return files, nil
}

func (thiz *Message) IsApiNamespace() bool {
	namespace := thiz.GetPathParam("namespace")
	if namespace == string(core.Api) {
		return true
	}
	return false
}

func (thiz *Message) Context() context.Context {
	if thiz.ctx != nil {
		return thiz.ctx
	}

	mCtx := &messageContext{}
	mCtx.url = thiz.GetRequest().URL
	mCtx.requestURI = thiz.GetRequest().RequestURI
	mCtx.cookieManager = thiz.cookieManager
	mCtx.headers = thiz.GetHeader()
	mCtx.request = thiz.GetRequest()
	mCtx.requestId = uuid.New().String()
	mCtx.ip = thiz.GetIp()
	//mCtx.applicationContext = thiz.applicationContext

	parentRequestCtx := thiz.GetRequest().Context()
	mCtx.requestCtx = parentRequestCtx

	msgCtx := context.WithValue(parentRequestCtx, core.MessageContextKey, mCtx)
	thiz.ctx = context.WithValue(msgCtx, core.DbSessionContextKey, db.Connection().WithContext(msgCtx)) // todo: Currently it can be only GORM Postgres DB

	mCtx.session = thiz.GetSession()

	return thiz.ctx
}

func (thiz *Message) WithContext(ctx context.Context) {
	if _, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); !ok {
		parentCtx := thiz.Context()
		ctx = context.WithValue(parentCtx, core.MessageContextKey, ctx)
	}
	thiz.ctx = ctx
}

func (thiz *Message) GetSession() core.ISession {
	if thiz.currentSession != nil {
		return thiz.currentSession
	}

	authStrategy := thiz.authContext.ResolveAuthStrategyByContext(thiz.Context())
	if authStrategy == nil {
		return nil
	}

	sessionId := authStrategy.ResolveSessionId(thiz.Context())
	if sessionId == "" {
		return nil
	}

	thiz.currentSession = thiz.sessionStorage.GetSessionById(sessionId)

	return thiz.currentSession
}

func (thiz *Message) GetCookieManager() core.ICookieManager {
	return thiz.cookieManager
}

func (thiz *Message) Close() error {
	thiz.clearOneTimeParams()
	for i := range thiz.io {
		err := thiz.io[i].Close()
		if err != nil {
			log.Log().Warnf("Error when closing stream: %v\n", err)
		}
	}
	thiz.request.Body.Close()

	if thiz.writer.(*ResponseWriterWrapper).Body != nil {
		thiz.writer.(*ResponseWriterWrapper).Body.Close()
	}

	return nil
}

func (thiz Message) GetInputParameters() []reflect.Value {
	return thiz.inputParameters
}

func (thiz *Message) addOptionsToView(options map[string]any) map[string]any {
	authStrategy := thiz.authContext.ResolveAuthStrategyByContext(thiz.Context())
	if authStrategy == nil {
		return nil
	}

	authUser, _ := thiz.authContext.ResolveAuthStrategyByContext(thiz.Context()).CurrentUser(thiz.Context())

	if authUser != nil {
		options["currentUsername"] = authUser.GetUsername()
	}

	return options
}

func (thiz *Message) GetIp() string {
	xff := thiz.request.Header.Get("X-Forwarded-For")
	if xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	xri := thiz.request.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	ip, _, err := net.SplitHostPort(thiz.request.RemoteAddr)
	if err != nil {
		return ""
	}

	return ip
}

func (thiz *Message) SetSession() {
	if thiz.GetRequest().Method == http.MethodOptions {
		return
	}

	currentAuthStrategy := thiz.authContext.ResolveAuthStrategyByContext(thiz.Context())
	if currentAuthStrategy == nil {
		return
	}

	session := currentAuthStrategy.CurrentSession(thiz.Context())

	if session != nil && !session.IsExpired() {
		session.SetExpiry(session.GetExpiry().Add(thiz.sessionStorage.GetSessionLifetime()))
		thiz.currentSession = session
		thiz.makeOneTimeParamsUsed()
		return
	}

	if session != nil && session.IsExpired() {
		thiz.sessionStorage.DeleteSession(session)
	}

	session, err := currentAuthStrategy.NewSessionWithoutUser(thiz.Context())
	if err != nil {
		err2.HandleError(err)
		return
	}

	thiz.currentSession = session

	ctx := thiz.Context()
	if msgCtx, ok := ctx.Value(core.MessageContextKey).(*messageContext); ok {
		if msgCtx.session == nil {
			msgCtx.session = session
		}
	}
}

func (thiz *Message) makeOneTimeParamsUsed() {
	item := thiz.GetSession().GetItem(core.OneTimeSessionAttributeKey)
	if item == "" {
		return
	}
	oneTimeParams := model.OneTimeParams{}
	err := json.Unmarshal([]byte(item), &oneTimeParams)
	if err != nil {
		err2.HandleError(err)
		return
	}

	oneTimeParams.Start = false

	buf, err := json.Marshal(oneTimeParams)
	if err != nil {
		err2.HandleError(err)
		return
	}

	thiz.GetSession().SetItem(core.OneTimeSessionAttributeKey, string(buf))
}

func (thiz *Message) clearOneTimeParams() {
	session := thiz.GetSession()
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

	thiz.GetSession().ClearItem(core.OneTimeSessionAttributeKey)
}
