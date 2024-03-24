package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/auth"
	"git.qix.sx/gorgany/gorgany.git/decoder"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/util"
	view2 "git.qix.sx/gorgany/gorgany.git/view"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"io"
	"mime/multipart"
	"net/http"
	url2 "net/url"
	"reflect"
	"strings"
	"time"
)

type Message struct {
	writer  http.ResponseWriter
	request *http.Request

	renderer *view2.EngineRenderer `container:"inject"`

	cachedQuery    *decoder.QueryParams
	currentSession core.ISession

	sessionStorage core.ISessionStorage `container:"inject"`
	cookieManager  *CookieManager

	ctx context.Context

	inputParameters []reflect.Value
}

func (thiz *Message) Init() {
	thiz.cookieManager = NewCookieManager(thiz.writer, thiz.request)
	thiz.setSession()
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
	err := thiz.renderer.DoRender(thiz.Context(), thiz.writer, template, options)
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

func (thiz Message) GetQuery() decoder.QueryParams {
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

	defer fileRequest.Close()

	return &model.File{
		Name:    header.Filename,
		Content: fileRequest,
		Size:    header.Size,
		Loaded:  true,
	}, nil
}

func (thiz *Message) GetFiles(key string) ([]core.IFile, error) {
	files := make([]core.IFile, 0)

	multipartForm := thiz.GetMultipartFormValues()
	filesRequest := multipartForm.File
	for mapKey, val := range filesRequest {
		if mapKey != key {
			continue
		}
		for _, file := range val {
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer reader.Close()

			files = append(files, &model.File{Name: file.Filename, Content: reader, Size: file.Size, Loaded: true})
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

	parentCtx := thiz.GetRequest().Context()
	mCtx.parentCtx = parentCtx

	thiz.ctx = context.WithValue(parentCtx, core.MessageContextKey, mCtx)

	mCtx.session = thiz.GetSession()

	return thiz.ctx
}

func (thiz *Message) GetSession() core.ISession {
	if thiz.currentSession != nil {
		return thiz.currentSession
	}

	authStrategy := auth.ResolveAuthStrategyByContext(thiz.Context())
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
	return nil
}

func (thiz Message) GetInputParameters() []reflect.Value {
	return thiz.inputParameters
}

func (thiz *Message) addOptionsToView(options map[string]any) map[string]any {
	authStrategy := auth.ResolveAuthStrategyByContext(thiz.Context())
	if authStrategy == nil {
		return nil
	}

	authUser, _ := auth.ResolveAuthStrategyByContext(thiz.Context()).CurrentUser(thiz.Context())

	if authUser != nil {
		options["currentUsername"] = authUser.GetUsername()
	}

	return options
}

func (thiz *Message) setSession() {
	if thiz.GetRequest().Method == "OPTIONS" {
		return
	}

	currentAuthStrategy := auth.ResolveAuthStrategyByContext(thiz.Context())
	if currentAuthStrategy == nil {
		return
	}

	session := currentAuthStrategy.CurrentSession(thiz.Context())

	if session != nil && !session.IsExpired() {
		session.SetExpiry(session.GetExpiry().Add(time.Duration(internal.GetFrameworkRegistrar().GetSessionLifetime()) * time.Second))
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
