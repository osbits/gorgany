package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

type SupportEndSlashMiddleware struct {
}

func (thiz SupportEndSlashMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		r := message.GetRequest()
		path := r.URL.Path

		if len(path) > 0 && path[len(path)-1] == '/' {
			r.URL.Path = path[:len(path)-1]
		}

		next(message)
	}
}
