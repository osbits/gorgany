package auth

import "gorgany/proxy"

var userService proxy.IUserService

func SetAuthEntityService(service proxy.IUserService) {
	userService = service
}

func GetAuthEntityService() proxy.IUserService {
	return userService
}
