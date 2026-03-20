package dto

import (
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/model"
	"github.com/osbits/gorgany/util"
	"reflect"
)

func ReturnObject(payload any, status core.HttpStatus, errors any) *model.ApiReturnObject { //todo status and errors
	dto := &model.ApiReturnObject{}
	dto.Body = payload
	dto.HttpStatus = status

	if errors != nil {
		e := reflect.ValueOf(errors)
		if util.IndirectValue(e).Kind() != reflect.Slice {
			dto.Errors = []any{errors}
		} else {
			dto.Errors = util.InterfaceSlice(errors)
		}
	}

	return dto
}
