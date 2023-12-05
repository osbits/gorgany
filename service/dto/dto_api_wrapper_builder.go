package dto

import (
	"gorgany"
	"gorgany/model"
	"gorgany/util"
	"reflect"
)

func ReturnObject(payload any, status gorgany.HttpStatus, errors any) *model.ApiReturnObject { //todo status and errors
	dto := &model.ApiReturnObject{}
	dto.Body = payload
	dto.HttpStatus = status

	if errors != nil {
		e := reflect.ValueOf(errors)
		if e.Kind() != reflect.Slice {
			dto.Errors = []any{errors}
		} else {
			dto.Errors = util.InterfaceSlice(errors)
		}
	}

	return dto
}
