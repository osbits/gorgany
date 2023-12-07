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
const SessionCookieName = "sessionToken"

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
	SuccessHttpStatus       HttpStatus = HttpStatus{Status: 200, Code: "SUCCESS"}
	CreatedHttpStatus       HttpStatus = HttpStatus{Status: 201, Code: "CREATED"}
	DeletedHttpStatus       HttpStatus = HttpStatus{Status: 204, Code: "DELETED"}
	BadRequestHttpStatus    HttpStatus = HttpStatus{Status: 400, Code: "BAD_REQUEST"}
	NotAuthorizedHttpStatus HttpStatus = HttpStatus{Status: 401, Code: "NOT_AUTHORIZED"}
	ForbiddenHttpStatus     HttpStatus = HttpStatus{Status: 403, Code: "FORBIDDEN"}
	NotFoundHttpStatus      HttpStatus = HttpStatus{Status: 404, Code: "NOT_FOUND"}
	ValidationHttpStatus    HttpStatus = HttpStatus{Status: 429, Code: "VALIDATION"}
	InternalErrorHttpStatus HttpStatus = HttpStatus{Status: 500, Code: "INTERNAL_ERROR"}
)

type ContentType string

const (
	ApplicationJson   = "application/json"
	MultipartFormData = "multipart/form-data"
)

const (
	GorganyFieldTag = "grgorm"
	ExtendsValue    = "extends"
)

var GlobalDateFormat = "2006-01-02"
var GlobalDateTimeFormat = "2006-01-02 15:04:05"
