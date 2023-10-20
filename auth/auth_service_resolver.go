package auth

import (
	"context"
	"gorgany/app/core"
)

// ctx - context with gorgany/http.Message instance
func ResolveAuthService(ctx context.Context) core.AuthService {
	message := ctx.Value("message").(core.HttpMessage)
	if message.IsApiNamespace() {
		return NewJwtService()
	}
	return GetSessionStorage()
}
