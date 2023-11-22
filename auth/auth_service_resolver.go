package auth

import (
	"context"
	"fmt"
	"gorgany"
	"gorgany/app/core"
)

// ResolveAuthService
// ctx - instance of core.IMessageContext
func ResolveAuthService(ctx context.Context) (core.AuthService, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("ctx is not core.IMessageContext instance")
	}

	ns := messageContext.GetPathParam("namespace")
	if ns == string(gorgany.Api) {
		return NewJwtService(), nil
	}
	return GetSessionStorage(), nil
}
