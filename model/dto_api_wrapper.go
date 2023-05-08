package model

type ApiResponseWrapper struct {
	Status     int    `json:"status"`      //todo const
	Errors     []any  `json:"errors"`      //todo struct
	StatusCode string `json:"status_code"` //todo const
	Body       any    `json:"body"`
}
