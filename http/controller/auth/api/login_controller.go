package api

import (
	"encoding/json"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/auth"
	"git.qix.sx/gorgany/gorgany.git/http/middleware"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"git.qix.sx/gorgany/gorgany.git/util"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct{}

type LoginPayload struct {
	Username string
	Password string
}

func (thiz LoginController) Login(message core.HttpMessage) {
	body := message.GetBody()

	loginPayload := &LoginPayload{}
	err := json.Unmarshal(body, loginPayload)
	if err != nil {
		panic(err)
	}

	user, err := auth.GetAuthEntityService().GetByUsername(loginPayload.Username)
	if err != nil {
		message.ResponseJSON(dto.ReturnObject("Unauthorized", core.NotAuthorizedHttpStatus, nil), 401)
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), loginPayload.Password) {
		message.ResponseJSON(dto.ReturnObject("Unauthorized", core.NotAuthorizedHttpStatus, nil), 401)
		return
	}

	token := message.Login(user, "jwt")
	if token == "" {
		message.ResponseJSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, "Token has not been generated!"), 200)
		return
	}

	responseBodyMap := make(map[string]string)

	responseBodyMap["access_token"] = token
	message.ResponseJSON(dto.ReturnObject(responseBodyMap, core.SuccessHttpStatus, nil), 200)
}

func (thiz LoginController) GetRoutes() []core.IRouteConfig {
	corsMiddleware := middleware.NewCorsMiddleware(middleware.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
	})

	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:        "/{namespace:api}/v1/login",
			Method:      core.POST,
			Handler:     thiz.Login,
			Middlewares: []core.IMiddleware{corsMiddleware},
		},
	}
}
