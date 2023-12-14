package auth

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

// ResolveAuthService
// ctx - instance of core.IMessageContext
func ResolveAuthService(ctx context.Context) (core.AuthService, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("ctx is not core.IMessageContext instance")
	}

	if messageContext.GetHeader().Get("Content-Type") == core.ApplicationJson || messageContext.GetPathParam("namespace") == "api" {
		return NewJwtService(), nil
	}
	return GetSessionStorage(), nil
}
