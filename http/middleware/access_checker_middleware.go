package middleware

import (
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/log"
	"github.com/osbits/gorgany/v2/service/dto"
)

type AccessCheckerMiddleware struct {
}

func (thiz AccessCheckerMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		var accessCheckerCommand core.HttpAccessCommand

		//args := message.GetInputParameters()
		//for _, arg := range args {
		//	if !util.IndirectType(arg.Type()).Implements(reflect.TypeOf((*core.HttpAccessCommand)(nil)).Elem()) {
		//		continue
		//	}
		//	accessCheckerCommand = arg.Interface().(core.HttpAccessCommand)
		//}

		if accessCheckerCommand == nil {
			log.Log().Warnf("AccessCheckerMiddleware is enabled for \u001B[0;32m%s\u001B[0m, but instance of \u001B[0;33mcore.HttpAccessCommand\u001B[0m is not injected to handler", message.Request().RawRequest().URL.Path)
			next(message)
			return
		}

		allowed := accessCheckerCommand.IsAccessAllowed(message.Context())
		if !allowed {
			if message.Request().PathParam("namespace") == "api" {
				message.Response().JSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, nil), 403)
				return
			}
			//todo
		}
		next(message)
	}
}
