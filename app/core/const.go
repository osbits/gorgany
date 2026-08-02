package core

import (
	"reflect"
	"strings"
)

// HTTP

type Method string

const (
	GET    Method = "GET"
	POST          = "POST"
	PUT           = "PUT"
	DELETE        = "DELETE"
)

// Gorgany ORM

const GorganyORMTag = "grgorm"
const GorganyORMPreload = "preload"
const GorganyORMExtends = "extends"

const FullMessageInstanceContextKey = "fullMessageInstance"
const MessageContextKey = "messageContext"
const DbSessionContextKey = "dbSession"

// SessionCookieBaseName is the session cookie's name without a prefix.
//
// Never read or write the cookie under this name directly. Which of the two names below is
// in use depends on configuration, and a component that writes one while another reads the
// other authenticates nobody and reports nothing — auth.SessionCookieName() is the single
// place that decides.
const SessionCookieBaseName = "GRG_SESSION_ID"

// HostPrefixedSessionCookieName is the __Host- form of the session cookie, and the default.
//
// The prefix is not decoration. A browser accepts a `__Host-` cookie only if it is Secure,
// has Path=/ and carries no Domain — and, the part that matters here, it will not let any
// other host set one for this host. An unprefixed name is a token any host under the
// registrable domain can also write: `sibling.example.com` sends
// `Set-Cookie: GRG_SESSION_ID=chosen; Domain=example.com; Path=/login`, RFC 6265 serialises
// the more specific path first, http.Request.Cookie returns the first match, and the
// framework reads the identifier the sibling picked — even though the app's own cookie is
// host-only. Assigning an authenticated user to an identifier the client chose is session
// fixation, and without the prefix an app could not opt out of it.
const HostPrefixedSessionCookieName = "__Host-" + SessionCookieBaseName

const OneTimeSessionAttributeKey = "_GORGANY_ONE_TIME_PARAMS"

const DefaultKeyInRegistrar = "default"

const OriginalURLPathKey = "originalPath"

// Error

const GeneralError = "GeneralError"

type DynamicAccessActionType string

const (
	Delete DynamicAccessActionType = "DELETE"
	Edit                           = "EDIT"
	Show                           = "SHOW" //action that are above also denied
	Create                         = "CREATE"
)

type HttpNamespace string

const (
	Api HttpNamespace = "api"
	Web               = "web"
)

type HttpStatus struct {
	Status int
	Code   string
}

var (
	SuccessHttpStatus       = HttpStatus{Status: 200, Code: "SUCCESS"}
	CreatedHttpStatus       = HttpStatus{Status: 201, Code: "CREATED"}
	DeletedHttpStatus       = HttpStatus{Status: 204, Code: "DELETED"}
	BadRequestHttpStatus    = HttpStatus{Status: 400, Code: "BAD_REQUEST"}
	NotAuthorizedHttpStatus = HttpStatus{Status: 401, Code: "NOT_AUTHORIZED"}
	ForbiddenHttpStatus     = HttpStatus{Status: 403, Code: "FORBIDDEN"}
	NotFoundHttpStatus      = HttpStatus{Status: 404, Code: "NOT_FOUND"}
	// MethodNotAllowedHttpStatus is what the router answers a method mismatch with.
	// chi's default was a bare 405 with no body (C2).
	MethodNotAllowedHttpStatus = HttpStatus{Status: 405, Code: "METHOD_NOT_ALLOWED"}
	ValidationHttpStatus       = HttpStatus{Status: 422, Code: "VALIDATION"}
	InternalErrorHttpStatus    = HttpStatus{Status: 500, Code: "INTERNAL_ERROR"}
)

type ContentType string

const (
	ApplicationJson   ContentType = "application/json"
	MultipartFormData ContentType = "multipart/form-data"
	Query             ContentType = "query"
)

func (thiz ContentType) String() string {
	return string(thiz)
}

const (
	GorganyFieldTag         = "grgorm"
	ExtendsValue            = "extends"
	GeneratedDomainTagValue = "generated"
)

var GlobalDateFormat = "2006-01-02"
var GlobalDateTimeFormat = "2006-01-02 15:04:05"

type MiddlewarePriority int

const (
	Low    MiddlewarePriority = iota // not implemented yet
	Medium                           // middleware is called after the input parameters are parsed and injected, so you can get the input parameters using message.GetInputParameters methods.
	High                             // middleware is called before the input parameters are parsed and before they are injected
)

type ContentDisposition string

const (
	Inline     ContentDisposition = "inline"
	Attachment ContentDisposition = "attachment"
	FormData   ContentType        = "form-data"
)

func (thiz ContentDisposition) String() string {
	return string(thiz)
}

type GeneralDataType string

const (
	Numeric GeneralDataType = "numeric"
	String  GeneralDataType = "string"
	Struct  GeneralDataType = "struct"
	Bool    GeneralDataType = "bool"
	Unknown GeneralDataType = "unknown"
)

func GeneralDataTypeOf(kind reflect.Kind) GeneralDataType {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return Numeric
	case reflect.String:
		return String
	case reflect.Bool:
		return Bool
	case reflect.Struct:
		return Struct
	default:
		return Unknown
	}
}

const GrgViewTag = "grgview"

type GrgViewTagKeyValuePair string

func (thiz GrgViewTagKeyValuePair) Key() GrgViewTagKey {
	return GrgViewTagKey(strings.Split(string(thiz), "=")[0])
}

func (thiz GrgViewTagKeyValuePair) Value() string {
	splitKeyValue := strings.Split(string(thiz), "=")
	if len(splitKeyValue) == 2 {
		return splitKeyValue[1]
	}
	return ""
}

type GrgViewTagKey string

const (
	GrgViewIndex GrgViewTagKey = "index"
	GrgViewList  GrgViewTagKey = "list"
	GrgViewEdit  GrgViewTagKey = "edit"
	GrgViewEnum  GrgViewTagKey = "enum"
	GrgViewType  GrgViewTagKey = "type"
)

type GrgViewTagValue string

const (
	GrgViewIgnore       GrgViewTagValue = "ignore"
	GrgViewShow         GrgViewTagValue = "show"
	GrgViewViewOnly     GrgViewTagValue = "viewOnly"
	GrgViewDate         GrgViewTagValue = "DATE"
	GrgViewDateTime     GrgViewTagValue = "DATE_TIME"
	GrgViewDateTextarea GrgViewTagValue = "TEXTAREA"
)

const DefaultLoginUrl = "/login"

// CSRFTokenHeader is the response header carrying the current CSRF token, and the
// request header a client sends it back in.
//
// It is set on every response to a request that carries a session, so a client can
// re-read it after each call. See docs/CSRF.md for the full client contract.
const CSRFTokenHeader = "X-CSRF-Token"

// CSRFSessionKey is the session item the CSRF token is stored under. It used to be
// declared twice under the same literal — once in auth, once in http/middleware — so
// renaming one would have silently disabled the check.
const CSRFSessionKey = "csrf_token"

// DefaultCSRFTokenPath is where CsrfController exposes the token endpoint a SPA calls
// on boot.
const DefaultCSRFTokenPath = "/csrf"
