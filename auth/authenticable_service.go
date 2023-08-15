package auth

import (
	"gorgany/internal"
	"gorgany/proxy"
)

func GetAuthEntityService() proxy.IUserService {
	return internal.GetFrameworkRegistrar().GetUserService()
}
