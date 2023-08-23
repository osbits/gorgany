package router

import (
	"gorgany/internal"
	"gorgany/proxy"
)

func GetRouter() proxy.Router {
	return internal.GetFrameworkRegistrar().GetRouter()
}
