package core

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/url"
)

type HttpMessage interface {
	GetRequest() *http.Request
	GetWriter() http.ResponseWriter
	GetPathParam(key string) string
	GetBody() []byte
	GetBodyContent() string
	GetHeader() http.Header
	GetCookie(key string) *http.Cookie
	Render(template string, options map[string]any)
	ResponseHeader() http.Header
	Response(responseBody string, statusCode int)
	ResponseJSON(responseBody any, statusCode int)
	ResponseBytes(responseBody []byte, statusCode int)
	SetCookie(cookie *http.Cookie)
	RedirectWithParams(url string, redirectCode int, params map[string]any)
	Redirect(url string, redirectCode int)
	OneTimeParams() map[string][]string
	GetOneTimeParam(key string) string
	ClearOneTimeParams()
	GetBearerToken() string
	GetQueryParam(key string) string
	GetQueryParams(key string) []string
	GetQueryParamsMap(key string) []map[string]string
	GetBodyParam(key string) any
	GetMultipartFormValues() *multipart.Form
	Locale() string
	GetFile(key string) (IFile, error)
	GetFiles(key string) ([]IFile, error)
	IsApiNamespace() bool
	Context() context.Context
	GetSession() ISession
	GetCookieManager() ICookieManager
}

type IMessageContext interface {
	GetURL() *url.URL
	GetRequestURL() string
	GetCookieManager() ICookieManager
	GetHeader() http.Header
	GetBearerToken() string
	GetPathParam(name string) string
	GetSession() ISession
	GetRequest() *http.Request

	GetParent() context.Context
}

type ISimpleStorage interface {
	GetItem(key string) string
	SetItem(key string, value string)
}

type ICookieManager interface {
	SetCookie(cookie *http.Cookie)
	GetCookie(key string) *http.Cookie
	GetCookies() []*http.Cookie
}
