package model

import "gorgany"

type ApiResponseWrapper struct {
	Status     int                    `json:"status"`
	Errors     []any                  `json:"errors"`
	StatusCode gorgany.HttpStatusCode `json:"status_code"`
	Body       any                    `json:"body"`
}
