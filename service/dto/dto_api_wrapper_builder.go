package dto

import (
	"gorgany"
	"gorgany/model"
	"gorgany/util"
)

func WrapPayload(payload any, status int, errors any) *model.ApiResponseWrapper { //todo status and errors
	dto := &model.ApiResponseWrapper{}
	dto.Body = payload
	dto.Status = status

	if errors != nil {
		dto.Errors = util.InterfaceSlice(errors)
	}

	switch dto.Status {
	case 200:
		dto.StatusCode = gorgany.Success
		break
	case 204:
		dto.StatusCode = gorgany.Deleted
		break
	case 400:
		dto.StatusCode = gorgany.BadRequest
		break
	case 401:
		dto.StatusCode = gorgany.NotAuthorized
		break
	case 403:
		dto.StatusCode = gorgany.Forbidden
		break
	case 404:
		dto.StatusCode = gorgany.NotFound
		break
	case 422:
		dto.StatusCode = gorgany.Validation
		break
	case 500:
		dto.StatusCode = gorgany.InternalError
		break
	}

	return dto
}
