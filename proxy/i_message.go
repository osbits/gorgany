package proxy

import (
	"gorgany/model"
	"mime/multipart"
	"net/http"
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
	Login(user model.Authenticable)
	Logout()
	IsLoggedIn() bool
	CurrentUser() (model.Authenticable, error)
	GetBearerToken() string
	GetQueryParam(key string) any
	GetBodyParam(key string) any
	GetMultipartFormValues() *multipart.Form
	Locale() string
	GetFile(key string) (*model.File, error)
	GetFiles(key string) ([]*model.File, error)
	IsApiNamespace() bool
}
