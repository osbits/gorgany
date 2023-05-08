package service

import "gorgany/model"

type IUserService interface {
	Get(id uint64) (model.Authenticable, error)
	GetByUsername(username string) (model.Authenticable, error)
	Save(authEntity model.Authenticable) error
}

var userService IUserService

func SetAuthEntityService(service IUserService) {
	userService = service
}

func GetAuthEntityService() IUserService {
	return userService
}
