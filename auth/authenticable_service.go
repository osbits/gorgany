package auth

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

func GetAuthEntityService() core.IUserService {
	return internal.GetApplicationContext().GetUserService()
}
