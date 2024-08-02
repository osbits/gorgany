package router

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

func GetRouter() core.Router {
	return internal.GetApplicationContext().GetRouter()
}
