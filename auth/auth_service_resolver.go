package auth

import (
	"context"
	"gorgany/proxy"
)

// ctx - context with gorgany/http.Message instance
func ResolveAuthService(ctx context.Context) AuthService {
	message := ctx.Value("message").(proxy.HttpMessage)
	if message.IsApiNamespace() {
		return NewJwtService()
	}

	return GetSessionStorage()
}
