package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"git.qix.sx/gorgany/gorgany.git/util"
	"reflect"
)

type AccessCheckerMiddleware struct {
}

func (thiz AccessCheckerMiddleware) Handle(message core.HttpMessage) bool {
	var accessCheckerCommand core.HttpAccessCommand

	args := message.GetInputParameters()
	for _, arg := range args {
		if !util.IndirectType(arg.Type()).Implements(reflect.TypeOf((*core.HttpAccessCommand)(nil)).Elem()) {
			continue
		}
		accessCheckerCommand = arg.Interface().(core.HttpAccessCommand)
	}

	if accessCheckerCommand == nil {
		log.Log().Warnf("AccessCheckerMiddleware is enabled for \u001B[0;32m%s\u001B[0m, but instance of \u001B[0;33mcore.HttpAccessCommand\u001B[0m is not injected to handler", message.GetRequest().URL.Path)
		return true
	}

	allowed := accessCheckerCommand.IsAccessAllowed(message.Context())
	if !allowed {
		if message.IsApiNamespace() {
			message.ResponseJSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, nil), 200)
			return false
		}
		//todo
	}
	return true
}

func (thiz AccessCheckerMiddleware) Priority() core.MiddlewarePriority {
	return core.Medium
}
