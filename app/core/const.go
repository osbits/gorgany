package core

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

const MessageContextKey = "messageContext"

const OneTimeParamsCookieName = "oneTimeParams"
const SessionCookieName = "GRG_SESSION_ID"
const OneTimeSessionAttributeKey = "_GORGANY_ONE_TIME_PARAMS"

const DefaultKeyInRegistrar = "default"

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
	ValidationHttpStatus    = HttpStatus{Status: 422, Code: "VALIDATION"}
	InternalErrorHttpStatus = HttpStatus{Status: 500, Code: "INTERNAL_ERROR"}
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
