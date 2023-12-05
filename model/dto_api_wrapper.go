package model

import (
	"encoding/json"
	"gorgany/app/core"
	"gorgany/err"
)

type ApiReturnObject struct {
	Errors     []any
	HttpStatus core.HttpStatus
	Body       any
}

func (thiz *ApiReturnObject) MarshalJSON() ([]byte, error) {
	tmpStruct := struct {
		Status int    `json:"status"`
		Code   string `json:"status_code"`
		Body   any    `json:"body"`
		Errors []any  `json:"errors"`
	}{}

	tmpStruct.Status = thiz.HttpStatus.Status
	tmpStruct.Code = thiz.HttpStatus.Code
	tmpStruct.Body = thiz.Body
	tmpStruct.Errors = thiz.Errors

	return json.Marshal(tmpStruct)
}

func (thiz *ApiReturnObject) AddValidationError(e err.ValidationError) {
	if thiz.HttpStatus != core.ValidationHttpStatus {
		thiz.HttpStatus = core.ValidationHttpStatus
	}
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) AddError(e any, status core.HttpStatus) {
	thiz.HttpStatus = core.ValidationHttpStatus
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) SetHttpStatus(status core.HttpStatus) {
	thiz.HttpStatus = status
}

func (thiz *ApiReturnObject) SetBody(body any) {
	thiz.Body = body
}

func (thiz *ApiReturnObject) HasErrors() bool {
	return len(thiz.Errors) > 0
}
