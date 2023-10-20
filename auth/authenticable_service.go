package auth

import (
	"gorgany/app/core"
	"gorgany/internal"
)

func GetAuthEntityService() core.IUserService {
	return internal.GetFrameworkRegistrar().GetUserService()
}
