package http

type ICrudController interface {
	Create(message Message)
	Store(message Message)
	//Show()
	//Edit()
	//Update()
	//Delete()
}

type IController interface {
	GetRoutes() []*RouteConfig
}

type Controllers []IController

func (thiz Controllers) AddController(controller IController) {
	thiz = append(thiz, controller)
}
