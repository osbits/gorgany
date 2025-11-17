package api

import (
	"encoding/json"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/http/middleware"
	"github.com/osbits/gorgany/http/router"
	"github.com/osbits/gorgany/service/dto"
	"github.com/osbits/gorgany/util"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct {
	authContext core.IAuthContext `container:"inject"`
	userService core.IUserService `container:"inject"`
}

type LoginPayload struct {
	Username string
	Password string
}

func (thiz LoginController) Login(message core.HttpMessage) {
	body, _ := message.Request().Body()

	loginPayload := &LoginPayload{}
	err := json.Unmarshal(body, loginPayload)
	if err != nil {
		panic(err)
	}

	user, err := thiz.userService.GetByUsername(loginPayload.Username)
	if err != nil {
		message.Response().JSON(dto.ReturnObject("Unauthorized", core.NotAuthorizedHttpStatus, nil), 401)
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), loginPayload.Password) {
		message.Response().JSON(dto.ReturnObject("Unauthorized", core.NotAuthorizedHttpStatus, nil), 401)
		return
	}

	session, err := thiz.authContext.Strategy("jwt").Login(user, message.Context())
	if session.GetId() == "" {
		message.Response().JSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, "Token has not been generated!"), 200)
		return
	}

	responseBodyMap := make(map[string]string)

	responseBodyMap["access_token"] = session.GetId()
	message.Response().JSON(dto.ReturnObject(responseBodyMap, core.SuccessHttpStatus, nil), 200)
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
