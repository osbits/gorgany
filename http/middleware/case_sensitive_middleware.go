package middleware

import (
	"context"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
)

type CaseSensitiveMiddleware struct {
}

func (thiz CaseSensitiveMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		r := message.Request().RawRequest()
		ctx := message.Context()

		ctx = context.WithValue(ctx, core.OriginalURLPathKey, r.URL.Path)

		// Modify the URL path directly on the request
		r.URL.Path = strings.ToLower(r.URL.Path)

		next(message)
	}
}
