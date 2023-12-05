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

type HttpStatus struct {
	Status int
	Code   string
}

var (
	Success       HttpStatus = HttpStatus{Status: 200, Code: "SUCCESS"}
	Deleted       HttpStatus = HttpStatus{Status: 204, Code: "DELETED"}
	BadRequest    HttpStatus = HttpStatus{Status: 400, Code: "BAD_REQUEST"}
	NotAuthorized HttpStatus = HttpStatus{Status: 401, Code: "NOT_AUTHORIZED"}
	Forbidden     HttpStatus = HttpStatus{Status: 403, Code: "FORBIDDEN"}
	NotFound      HttpStatus = HttpStatus{Status: 404, Code: "NOT_FOUND"}
	Validation    HttpStatus = HttpStatus{Status: 429, Code: "VALIDATION"}
	InternalError HttpStatus = HttpStatus{Status: 429, Code: "INTERNAL_ERROR"}
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
