package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"git.qix.sx/gorgany/gorgany.git/util"
	"reflect"
)

type AccessCheckerMiddleware struct {
}

func (thiz AccessCheckerMiddleware) Handle(message core.HttpMessage) bool {
	var accessCheckerCommand core.HttpAccessCommand

	args := message.GetArgs()
	for _, arg := range args {
		if !util.IndirectType(arg.Type()).Implements(reflect.TypeOf((*core.HttpAccessCommand)(nil)).Elem()) {
			continue
		}
		accessCheckerCommand = arg.Interface().(core.HttpAccessCommand)
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
