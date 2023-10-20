package router

import (
	"gorgany/app/core"
	"gorgany/internal"
)

func GetRouter() core.Router {
	return internal.GetFrameworkRegistrar().GetRouter()
}
