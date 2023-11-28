package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany"
	"gorgany/app/core"
	"gorgany/auth"
	"gorgany/log"
	"gorgany/model"
	"gorgany/util"
	view2 "gorgany/view"
	"io"
	"mime/multipart"
	"net/http"
	url2 "net/url"
	"reflect"
	"strings"
	"time"
)

type Message struct {
	writer   http.ResponseWriter
	request  *http.Request
	renderer *view2.EngineRenderer `container:"inject"`
}

func (thiz Message) Init() {
	thiz.renderer.Ctx = thiz.Context()
}

func (thiz Message) GetRequest() *http.Request {
	return thiz.request
}

func (thiz Message) GetWriter() http.ResponseWriter {
	return thiz.writer
}

func (thiz Message) GetPathParam(key string) string {
	return chi.URLParam(thiz.request, key)
}

// GetBody returns body in bytes
func (thiz Message) GetBody() []byte {
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
func (thiz Message) GetBodyContent() string {
	body := thiz.GetBody()
	return string(body)
}

func (thiz Message) GetHeader() http.Header {
	return thiz.request.Header
}

func (thiz Message) GetCookie(key string) (*http.Cookie, error) {
	cookie, err := thiz.request.Cookie(key)
	if err != nil {
		return nil, err
	}
	return cookie, nil
}

func (thiz Message) Render(template string, options map[string]any) {
	if options == nil {
		options = make(map[string]any)
	}
	oneTimeParams := thiz.OneTimeParams()
	for key, values := range oneTimeParams {
		options[key] = values
	}

	options = thiz.addOptionsToView(options)
	err := thiz.renderer.DoRender(thiz.writer, template, options)
	if err != nil {
		panic(fmt.Errorf("Error during render template '%s', %v", template, err))
	}
}

func (thiz Message) ResponseHeader() http.Header {
	return thiz.writer.Header()
}

func (thiz Message) Response(responseBody string, statusCode int) {
	thiz.writer.WriteHeader(statusCode)
	_, err := thiz.writer.Write([]byte(responseBody))
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", responseBody, err))
	}
}

func (thiz Message) ResponseJSON(responseBody any, statusCode int) {
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

func (thiz Message) ResponseBytes(responseBody []byte, statusCode int) {
	thiz.writer.WriteHeader(statusCode)
	_, err := thiz.writer.Write(responseBody)
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", string(responseBody), err))
	}
}

func (thiz Message) SetCookie(key string, value string, expiresIn int) {
	cookie := &http.Cookie{
		Name:     key,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(time.Duration(expiresIn) * time.Second),
		Secure:   false,
		HttpOnly: true,
	}
	http.SetCookie(thiz.writer, cookie)
}

func (thiz Message) RedirectWithParams(url string, redirectCode int, params map[string]any) {
	oneTimeParams := url2.Values{}

	addToValues := func(key string, value any, otParams *url2.Values) {
		var val string

		str, ok := value.(fmt.Stringer)
		if ok {
			val = str.String()
		} else {
			val = fmt.Sprintf("%v", value)
		}

		oneTimeParams.Add(key, fmt.Sprintf("%v", val))
	}

	for key, value := range params {
		kind := reflect.TypeOf(value)
		if kind.Kind() == reflect.Slice {
			slice := util.InterfaceSlice(value)
			for _, sliceValue := range slice {
				addToValues(key, sliceValue, &oneTimeParams)
			}
		} else {
			addToValues(key, value, &oneTimeParams)
		}
	}
	thiz.SetCookie(core.OneTimeParamsCookieName, oneTimeParams.Encode(), 1)

	url = util.AddLocaleToURL(thiz.Locale(), url)
	http.Redirect(thiz.writer, thiz.request, url, redirectCode)
}

func (thiz Message) Redirect(url string, redirectCode int) {
	url = util.AddLocaleToURL(thiz.Locale(), url)
	http.Redirect(thiz.writer, thiz.request, url, redirectCode)
}

func (thiz Message) OneTimeParams() map[string][]string {
	oneTimeParams := url2.Values{}

	cookie, err := thiz.request.Cookie(core.OneTimeParamsCookieName)
	if err != nil {
		if strings.Contains(err.Error(), "named cookie not present") {
			return oneTimeParams
		}
		panic(err)
	}

	oneTimeParams, err = url2.ParseQuery(cookie.Value)
	if err != nil {
		panic(err) //todo
	}
	return oneTimeParams
}

func (thiz Message) GetOneTimeParam(key string) string {
	oneTimeParams := thiz.OneTimeParams()
	val, ok := oneTimeParams[key]
	if !ok {
		return ""
	}
	return val[0]
}

func (thiz Message) ClearOneTimeParams() {
	thiz.SetCookie(core.OneTimeParamsCookieName, "", 1)
}

func (thiz Message) Login(user core.Authenticable) {
	sessionStorage := auth.GetSessionStorage()
	sessionToken, expiresAt, err := sessionStorage.NewSession(user)
	if err != nil {
		panic(err) //todo
	}
	thiz.SetCookie(core.SessionCookieName, sessionToken, int(expiresAt.Sub(time.Now()).Seconds()))
}

func (thiz Message) Logout() {
	sessionStorage := auth.GetSessionStorage()
	sessionStorage.Logout(thiz.Context())
	thiz.SetCookie(core.SessionCookieName, "", 10)
}

func (thiz Message) IsLoggedIn() bool {
	sessionStorage := auth.GetSessionStorage()
	return sessionStorage.IsLoggedIn(thiz.Context())
}

func (thiz Message) CurrentUser() (core.Authenticable, error) {
	authService, err := auth.ResolveAuthService(thiz.Context())
	authUser, err := authService.CurrentUser(thiz.Context())
	return authUser, err
}

func (thiz Message) GetBearerToken() string {
	bearerToken := thiz.GetHeader().Get("Authorization")
	return util.ParseBearerToken(bearerToken)
}

func (thiz Message) GetQueryParam(key string) any {
	values, err := url2.ParseQuery(thiz.request.URL.RawQuery)
	if err != nil {
		return "" //todo log
	}
	return values.Get(key)
}

func (thiz Message) GetBodyParam(key string) any {
	parsedBody := make(map[string]any)
	contentType := thiz.GetHeader().Get("Content-Type")
	if contentType == "application/json" {
		err := json.Unmarshal(thiz.GetBody(), &parsedBody)
		if err != nil {
			return "" //todo log
		}
		return parsedBody[key]
	}
	log.Log("").Warnf("http.Message: GetBodyParam is not implemented for %s yet", contentType)
	return ""
}

func (thiz Message) GetMultipartFormValues() *multipart.Form {
	err := thiz.request.ParseMultipartForm(10000) //todo
	if err != nil {
		return nil
	}
	return thiz.request.MultipartForm
}

func (thiz Message) Locale() string {
	lang := chi.URLParam(thiz.request, "lang")
	if lang == "" {
		lang = viper.GetString("i18n.lang.default")
	}
	return lang
}

func (thiz Message) GetFile(key string) (core.IFile, error) {
	thiz.GetMultipartFormValues()
	fileRequest, header, err := thiz.request.FormFile(key)
	if err != nil {
		if strings.Contains(err.Error(), "no such file") {
			return nil, nil
		}
		return nil, err
	}

	content, err := io.ReadAll(fileRequest)
	if err != nil {
		return nil, err
	}

	return &model.File{
		Name:    header.Filename,
		Content: string(content),
		Size:    header.Size,
	}, nil
}

func (thiz Message) GetFiles(key string) ([]core.IFile, error) {
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
			content, err := io.ReadAll(reader)
			if err != nil {
				return nil, err
			}
			files = append(files, &model.File{Name: file.Filename, Content: string(content), Size: file.Size})
		}
	}
	return files, nil
}

func (thiz Message) IsApiNamespace() bool {
	namespace := thiz.GetPathParam("namespace")
	if namespace == string(gorgany.Api) {
		return true
	}
	return false
}

func (thiz Message) Context() context.Context {
	messageContext := MessageContext{}
	messageContext.URL = thiz.GetRequest().URL
	messageContext.RequestURI = thiz.GetRequest().RequestURI
	messageContext.Cookies = thiz.GetRequest().Cookies()
	messageContext.Headers = thiz.GetHeader()

	parentRequest := thiz.GetRequest().Context()
	messageContext.Parent = parentRequest

	return context.WithValue(parentRequest, core.MessageContextKey, messageContext)
}

func (thiz Message) addOptionsToView(options map[string]any) map[string]any {
	authUser, _ := thiz.CurrentUser()

	if authUser != nil {
		options["currentUsername"] = authUser.GetUsername()
	}

	return options
}
