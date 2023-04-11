package http

import (
	"net/http"
)

type HandlerFunc func(message Message)

func Dispatch(w http.ResponseWriter, r *http.Request, handler HandlerFunc) {
	message := Message{
		writer:  w,
		request: r,
	}
	handler(message)
}
