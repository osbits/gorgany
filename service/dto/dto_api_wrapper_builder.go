package dto

import "gorgany/model"

func WrapPayload(payload any, status int, errors []any) *model.ApiResponseWrapper { //todo status and errors
	dto := &model.ApiResponseWrapper{}
	dto.Body = payload
	dto.Status = status
	dto.Errors = errors

	switch dto.Status {
	case 200:
		dto.StatusCode = "SUCCESS"
		break
	case 400:
		dto.StatusCode = "BAD_REQUEST"
		break
	case 401:
		dto.StatusCode = "Not authorized"
		break
	case 403:
		dto.StatusCode = "FORBIDDEN"
	case 404:
		dto.StatusCode = "NOT_FOUND"
		break
	case 429:
		dto.StatusCode = "VALIDATION"
		break
	case 500:
		dto.StatusCode = "INTERNAL_ERROR"
		break
	}

	return dto
}
