package http

import (
	"fmt"
	error2 "graecoFramework/error"
	"net/http"
)

type HandlerFunc func(message Message)

func Dispatch(w http.ResponseWriter, r *http.Request, handler HandlerFunc) {
	message := Message{
		writer:  w,
		request: r,
	}

	defer func() {
		if r := recover(); r != nil {
			Catch(r, message)
		}
	}()

	handler(message)
}

func Catch(err any, message Message) {
	error2.Catch(err)

	concreteError, ok := err.(error2.ValidationError)
	if ok {
		req := message.GetRequest()
		message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
		return
	}
	//todo 500 view
	message.Response(fmt.Sprintf("Oops... 500 error.\n %v", err), 500)
}
