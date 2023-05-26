package gorgany

type DynamicAccessActionType string

const (
	Delete DynamicAccessActionType = "DELETE"
	Edit                           = "EDIT"
	Show                           = "SHOW" //action that are above also denied
	Create                         = "CREATE"
)

type UserRole string

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
