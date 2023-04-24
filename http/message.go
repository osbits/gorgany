package http

import (
	"fmt"
	"github.com/go-chi/chi"
	"graecoFramework/provider/view"
	"graecoFramework/util"
	"io"
	"net/http"
	url2 "net/url"
	"reflect"
	"strings"
	"time"
)

const OneTimeParamsCookieName = "oneTimeParams"

type Message struct {
	writer  http.ResponseWriter
	request *http.Request
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

func (thiz Message) GetCookies() []*http.Cookie {
	return thiz.request.Cookies()
}

func (thiz Message) Render(template string, options map[string]any) {
	oneTimeParams := thiz.OneTimeParams()
	for key, values := range oneTimeParams {
		options[key] = values
	}

	err := view.Engine.Render(thiz.writer, template, options)
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
	thiz.SetCookie(OneTimeParamsCookieName, oneTimeParams.Encode(), 1)

	http.Redirect(thiz.writer, thiz.request, url, redirectCode)
}

func (thiz Message) OneTimeParams() map[string][]string {
	oneTimeParams := url2.Values{}

	cookie, err := thiz.request.Cookie(OneTimeParamsCookieName)
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
	thiz.SetCookie(OneTimeParamsCookieName, "", 10)
}
