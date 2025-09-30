package command

import "github.com/gorganyio/gorgany/app/core"

type BulkCommand struct {
	Requests []SpecificRequestCommand `json:"requests" validate:"required,dive,min=1,max=10"`
}

func (thiz BulkCommand) ContentType() core.ContentType {
	return core.ApplicationJson
}

type SpecificRequestCommand struct {
	Name   string         `json:"name" validate:"required"`
	Method string         `json:"method" validate:"required,oneof=POST GET PUT DELETE HEAD OPTIONS"`
	Params map[string]any `json:"params"`
	Body   string         `json:"body"`
}

func (thiz SpecificRequestCommand) ContentType() core.ContentType {
	return core.ApplicationJson
}
