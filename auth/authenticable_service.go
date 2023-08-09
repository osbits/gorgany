package auth

import (
	"context"
	"gorgany/proxy"
)

type IUserService interface {
	Get(id uint64) (proxy.Authenticable, error)
	GetByUsername(username string) (proxy.Authenticable, error)
	Save(authEntity proxy.Authenticable) error
}

var userService IUserService

func SetAuthEntityService(service IUserService) {
	userService = service
}

func GetAuthEntityService() IUserService {
	return userService
}

type AuthService interface {
	CurrentUser(ctx context.Context) (proxy.Authenticable, error)
}
