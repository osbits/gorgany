package router

import "gorgany/proxy"

var router proxy.Router

func GetRouter() proxy.Router {
	return router
}

func SetRouter(r proxy.Router) {
	router = r
}
