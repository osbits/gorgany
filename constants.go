package graecoFramework

type DynamicAccessActionType string

const (
	Edit   DynamicAccessActionType = "EDIT"
	Show                           = "SHOW" //action that are above also denied
	Fields                         = "FIELDS"
)
