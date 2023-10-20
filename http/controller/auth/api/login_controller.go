package api

import (
	"encoding/json"
	"gorgany/app/core"
	"gorgany/auth"
	"gorgany/http"
	"gorgany/http/middleware"
	"gorgany/http/router"
	"gorgany/service/dto"
	"gorgany/util"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct{}

type LoginPayload struct {
	Username string
	Password string
}

func (thiz LoginController) Login(message http.Message) {
	body := message.GetBody()

	loginPayload := &LoginPayload{}
	err := json.Unmarshal(body, loginPayload)
	if err != nil {
		panic(err)
	}

	user, err := auth.GetAuthEntityService().GetByUsername(loginPayload.Username)
	if err != nil {
		message.ResponseJSON(dto.WrapPayload("Unauthorized", 401, nil), 401)
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), loginPayload.Password) {
		message.ResponseJSON(dto.WrapPayload("Unauthorized", 401, nil), 401)
		return
	}

	jwtService := auth.NewJwtService()
	token, err := jwtService.GenerateJwt(user)
	if err != nil {
		panic(err)
	}

	responseBodyMap := make(map[string]string)

	responseBodyMap["access_token"] = token
	message.ResponseJSON(dto.WrapPayload(responseBodyMap, 200, nil), 200)
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
