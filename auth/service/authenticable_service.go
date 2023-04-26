package service

type Authenticable interface {
	GetUsername() string
	GetPassword() string
}

type IUserService interface {
	Get(id uint64) (Authenticable, error)
	GetByUsername(username string) (Authenticable, error)
	Save(authEntity Authenticable) error
}

var userService IUserService

func SetAuthEntityService(service IUserService) {
	userService = service
}

func GetAuthEntityService() IUserService {
	return userService
}
