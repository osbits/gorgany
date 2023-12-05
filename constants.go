package gorgany

const FrameworkVersion = "1.0"

type ExecType string

const (
	Server ExecType = "server"
	Cli    ExecType = "cli"
)

type RunMode string

const (
	Dev  RunMode = "dev"
	Prod         = "prod"
)

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

type HttpStatusCode string

const (
	Success       HttpStatusCode = "SUCCESS"
	Deleted                      = "DELETED"
	BadRequest                   = "BAD_REQUEST"
	NotAuthorized                = "NOT_AUTHORIZED"
	Forbidden                    = "FORBIDDEN"
	NotFound                     = "NOT_FOUND"
	Validation                   = "VALIDATION"
	InternalError                = "InternalError"
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
