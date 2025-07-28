package core

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// IWebContext defines the interface for web context management
type IWebContext interface {
	// SetHomeUrl sets the base URL for the application
	SetHomeUrl(url string)
	// GetHomeUrl returns the base URL of the application
	GetHomeUrl() string

	// SetNewMessage sets the factory function for creating new HTTP messages
	SetNewMessage(factory MessageFactory)
	// GetNewMessage returns the factory function for creating new HTTP messages
	GetNewMessage() MessageFactory

	// SetNewInputResolver sets the factory function for resolving input parameters
	SetNewInputResolver(factory InputResolverFactory)
	// GetNewInputResolver returns the factory function for resolving input parameters
	GetNewInputResolver() InputResolverFactory

	// AddController adds a new controller to the web context
	AddController(ctrl IController)
	// GetControllers returns all registered controllers
	GetControllers() []IController

	// AddMiddleware adds a new middleware configuration
	AddMiddleware(reg IMiddlewareConfig)
	// GetMiddlewares returns all registered middleware configurations
	GetMiddlewares() []IMiddlewareConfig

	// SetNotFound sets the handler for 404 Not Found responses
	SetNotFound(h HandlerFunc)
	// GetNotFound returns the handler for 404 Not Found responses
	GetNotFound() HandlerFunc
}

// MessageFactory is a function type that creates new HTTP messages
type MessageFactory func(http.ResponseWriter, *http.Request) (HttpMessage, error)

// InputResolverFactory is a function type that resolves input parameters for handlers
type InputResolverFactory func(HandlerFunc, HttpMessage) (interface{}, error)

// IRequestScope defines methods for reading HTTP requests.
type IRequestScope interface {
	Locale() string
	PathParam(name string) string
	QueryParam(key string) string
	QueryParams(key string) []string
	QueryParamsMap(key string) []map[string]string
	RawQuery() string
	Header() http.Header
	Body() ([]byte, error)
	BodyReader() io.ReadCloser
	FormFile(key string) (IFile, error)
	GetFiles(key string) ([]IFile, error)
	GetMultipartFormValues() *multipart.Form
	IP() string
	Query() QueryParams

	RawRequest() *http.Request
}

// IResponseScope defines methods for writing HTTP responses.
type IResponseScope interface {
	SetHeader(key, value string)
	Header() http.Header
	Text(body string, code int)
	JSON(v interface{}, code int)
	Bytes(b []byte, code int)
	Redirect(url string, code int)

	RawWriter() http.ResponseWriter
}

// IViewScope defines methods for rendering templates.
type IViewScope interface {
	Render(tpl string, data map[string]any)
}

// ISessionScope defines methods for session management and flash data.
type ISessionScope interface {
	Get() ISession
	ClearExpiredFlash()
}

// HttpMessage defines the full HTTP facade for controllers and dispatcher.
type HttpMessage interface {
	io.Closer
	// Access request data
	Request() IRequestScope
	// Access response writer
	Response() IResponseScope
	// Access session scope
	Session() ISessionScope
	// Access view renderer
	View() IViewScope
	// Access cookie manager
	Cookie() ICookieManager
	// Underlying context
	Context() context.Context
	// Redirect with flash data
	RedirectWithFlash(url string, code int, data map[string]interface{})
}

// IMessageContext defines the interface for message context operations
type IMessageContext interface {
	// GetURL returns the parsed URL
	GetURL() *url.URL
	// GetRequestURL returns the request URL as a string
	GetRequestURL() string
	// GetCookieManager returns the cookie manager
	GetCookieManager() ICookieManager
	// GetHeader returns the request headers
	GetHeader() http.Header
	// GetPathParam retrieves a path parameter by name
	GetPathParam(name string) string
	// GetSession returns the current session
	GetSession() ISession
	// GetRequest returns the underlying HTTP request
	GetRequest() *http.Request
	// GetRequestId returns the unique request identifier
	GetRequestId() string
	// GetIp returns the client's IP address
	GetIp() string
	// GetRequestContext returns the request context
	GetRequestContext() context.Context
}

// ISimpleStorage defines the interface for simple key-value storage
type ISimpleStorage interface {
	// GetItem retrieves a value by key
	GetItem(key string) string
	// SetItem sets a value for a key
	SetItem(key string, value string)
	// ClearItem removes a specific item by key
	ClearItem(attribute string)
	// ClearItems removes all items
	ClearItems()
}

// ICookieManager defines the interface for cookie management
type ICookieManager interface {
	// SetCookie sets a cookie in the response
	SetCookie(cookie *http.Cookie)
	// GetCookie retrieves a cookie by key
	GetCookie(key string) *http.Cookie
	// GetCookies returns all cookies
	GetCookies() []*http.Cookie
}

// HttpCommand defines the interface for HTTP command operations
type HttpCommand interface {
	// ContentType returns the content type of the command
	ContentType() ContentType
}

// HttpFilterCommand defines the interface for HTTP filter operations
type HttpFilterCommand interface {
	// AllowFilterFields returns the list of fields that can be filtered
	AllowFilterFields(ctx context.Context) []string
}

// HttpAccessCommand defines the interface for HTTP access control. Deprecated
type HttpAccessCommand interface {
	// IsAccessAllowed checks if access is allowed for the current context
	IsAccessAllowed(ctx context.Context) bool
	// FilterBuilder returns a query builder for filtering
	FilterBuilder(ctx context.Context) IQueryBuilder
}

// MapInitiator defines the interface for map initialization
type MapInitiator interface {
	// ValueOfMap initializes the struct with values from a map
	ValueOfMap(params map[string]string) error
}

// QueryParams defines the interface for query parameter operations
type QueryParams interface {
	// GetString retrieves a string parameter by key
	GetString(key string) string
	// GetArray retrieves an array of parameters by key
	GetArray(key string) []string
	// GetArrayMap retrieves an array of maps by key
	GetArrayMap(key string) []map[string]string
	// AsMap returns all parameters as a map
	AsMap() map[string]any
}
