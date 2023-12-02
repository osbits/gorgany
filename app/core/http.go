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
	GetCookie(key string) (*http.Cookie, error)
	Render(template string, options map[string]any)
	ResponseHeader() http.Header
	Response(responseBody string, statusCode int)
	ResponseJSON(responseBody any, statusCode int)
	ResponseBytes(responseBody []byte, statusCode int)
	SetCookie(key string, value string, expiresIn int)
	RedirectWithParams(url string, redirectCode int, params map[string]any)
	Redirect(url string, redirectCode int)
	OneTimeParams() map[string][]string
	GetOneTimeParam(key string) string
	ClearOneTimeParams()
	Login(user Authenticable)
	Logout()
	IsLoggedIn() bool
	CurrentUser() (Authenticable, error)
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
}

type IMessageContext interface {
	GetURL() *url.URL
	GetRequestURL() string
	GetCookies() []*http.Cookie
	GetCookie(name string) *http.Cookie
	GetHeader() http.Header
	GetSessionToken() string
	GetBearerToken() string
	GetPathParam(name string) string

	GetParent() context.Context
}
