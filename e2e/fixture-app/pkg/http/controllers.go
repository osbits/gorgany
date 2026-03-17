package fixturehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/http/middleware"
	"github.com/osbits/gorgany/http/router"
	"github.com/osbits/gorgany/service/dto"
	"github.com/osbits/gorgany/util"
	"github.com/spf13/viper"

	fixtureservice "github.com/osbits/gorgany/e2e/fixture-app/pkg/service"
)

func NewWebController() *WebController {
	return &WebController{}
}

type WebController struct {
	AuthContext core.IAuthContext `container:"inject"`
	UserService core.IUserService `container:"inject"`
}

func (c *WebController) Health(message core.HttpMessage) {
	message.Response().JSON(dto.ReturnObject(map[string]any{
		"status":   "ok",
		"greeting": viper.GetString("app.fixture.greeting"),
	}, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *WebController) Hello(message core.HttpMessage, name string) {
	message.Response().JSON(dto.ReturnObject(map[string]any{
		"message": fmt.Sprintf("hello %s", name),
	}, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *WebController) LoginPage(message core.HttpMessage) {
	message.Response().Text("fixture login page", http.StatusOK)
}

func (c *WebController) Login(message core.HttpMessage) {
	body, _ := message.Request().Body()
	values, _ := url.ParseQuery(string(body))
	username := strings.TrimSpace(values.Get("username"))
	password := values.Get("password")
	if username == "" || password == "" {
		message.Response().Text("username and password are required", http.StatusBadRequest)
		return
	}

	user, err := c.UserService.GetByUsername(username)
	if err != nil || user == nil || !util.CompareSaltedHash(user.GetPassword(), password) {
		message.Response().Text("invalid credentials", http.StatusUnauthorized)
		return
	}

	if _, err := c.AuthContext.ResolveAuthStrategyByContext(message.Context()).Login(user, message.Context()); err != nil {
		message.Response().Text("login failed", http.StatusInternalServerError)
		return
	}

	message.Response().Text("logged in", http.StatusOK)
}

func (c *WebController) Protected(message core.HttpMessage) {
	user, err := c.AuthContext.ResolveAuthStrategyByContext(message.Context()).CurrentUser(message.Context())
	if err != nil || user == nil {
		message.Response().Text("unauthorized", http.StatusUnauthorized)
		return
	}

	message.Response().Text(fmt.Sprintf("protected:%s:%s", user.GetUsername(), user.GetRole()), http.StatusOK)
}

func (c *WebController) Logout(message core.HttpMessage) {
	c.AuthContext.ResolveAuthStrategyByContext(message.Context()).Logout(message.Context())
	message.Response().Text("logged out", http.StatusOK)
}

func (c *WebController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:    "/health",
			Method:  core.GET,
			Handler: c.Health,
			Name:    "fixture.health",
		},
		&router.RouteConfig{
			Path:    "/hello/{name}",
			Method:  core.GET,
			Handler: c.Hello,
			Name:    "fixture.hello",
		},
		&router.RouteConfig{
			Path:    "/session/login",
			Method:  core.GET,
			Handler: c.LoginPage,
			Name:    "fixture.login.page",
		},
		&router.RouteConfig{
			Path:    "/session/login",
			Method:  core.POST,
			Handler: c.Login,
			Name:    "fixture.login",
		},
		&router.RouteConfig{
			Path:        "/web/protected",
			Method:      core.GET,
			Handler:     c.Protected,
			Middlewares: []core.IMiddleware{&middleware.AuthMiddleware{}},
			Name:        "fixture.protected",
		},
		&router.RouteConfig{
			Path:    "/session/logout",
			Method:  core.POST,
			Handler: c.Logout,
			Name:    "fixture.logout",
		},
	}
}

func NewAPIController() *APIController {
	return &APIController{}
}

type APIController struct {
	AuthContext   core.IAuthContext             `container:"inject"`
	UserService   core.IUserService             `container:"inject"`
	WidgetService *fixtureservice.WidgetService `container:"inject"`
}

func (c *APIController) Login(message core.HttpMessage, payload JwtLoginRequest) {
	user, err := c.UserService.GetByUsername(payload.Username)
	if err != nil || user == nil || !util.CompareSaltedHash(user.GetPassword(), payload.Password) {
		message.Response().JSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, "invalid credentials"), http.StatusUnauthorized)
		return
	}

	session, err := c.AuthContext.Strategy("api").Login(user, message.Context())
	if err != nil || session == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, "token generation failed"), http.StatusInternalServerError)
		return
	}

	message.Response().JSON(dto.ReturnObject(map[string]string{
		"access_token": session.GetId(),
	}, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) Protected(message core.HttpMessage) {
	user, err := c.AuthContext.Strategy("api").CurrentUser(message.Context())
	if err != nil || user == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, nil), http.StatusUnauthorized)
		return
	}

	message.Response().JSON(dto.ReturnObject(map[string]any{
		"username": user.GetUsername(),
		"role":     user.GetRole(),
	}, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) ListWidgets(message core.HttpMessage) {
	widgets, err := c.WidgetService.List()
	if err != nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}

	message.Response().JSON(dto.ReturnObject(widgets, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) CreateWidget(message core.HttpMessage, payload WidgetPayload) {
	user, err := c.AuthContext.Strategy("api").CurrentUser(message.Context())
	if err != nil || user == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, nil), http.StatusUnauthorized)
		return
	}

	widget, err := c.WidgetService.Create(payload.Name, payload.Description, user.GetId())
	if err != nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}

	message.Response().JSON(dto.ReturnObject(widget, core.CreatedHttpStatus, nil), http.StatusCreated)
}

func (c *APIController) GetWidget(message core.HttpMessage, id string) {
	widget, err := c.WidgetService.Get(id)
	if err != nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}
	if widget == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, "widget not found"), http.StatusNotFound)
		return
	}

	message.Response().JSON(dto.ReturnObject(widget, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) UpdateWidget(message core.HttpMessage, id string, payload WidgetPayload) {
	widget, err := c.WidgetService.Update(id, payload.Name, payload.Description)
	if err != nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}
	if widget == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, "widget not found"), http.StatusNotFound)
		return
	}

	message.Response().JSON(dto.ReturnObject(widget, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) UpdateWidgetTags(message core.HttpMessage, id string, payload WidgetRelationsPayload) {
	widget, err := c.WidgetService.UpdateTags(id, payload.TagIDs)
	if err != nil {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}
	if widget == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, "widget not found"), http.StatusNotFound)
		return
	}

	message.Response().JSON(dto.ReturnObject(widget, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) JSONEcho(message core.HttpMessage, payload JSONEchoRequest) {
	message.Response().JSON(dto.ReturnObject(payload, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) QueryEcho(message core.HttpMessage, payload QueryEchoRequest) {
	message.Response().JSON(dto.ReturnObject(payload, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) Upload(message core.HttpMessage, payload UploadRequest) {
	if payload.File == nil {
		message.Response().JSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, "file is required"), http.StatusBadRequest)
		return
	}

	message.Response().JSON(dto.ReturnObject(map[string]any{
		"title": payload.Title,
		"file":  payload.File.GetName(),
	}, core.SuccessHttpStatus, nil), http.StatusOK)
}

func (c *APIController) GetRoutes() []core.IRouteConfig {
	apiAuth := &middleware.AuthMiddleware{AuthStrategies: []string{"api"}}
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:      "/v1/login",
			Method:    core.POST,
			Handler:   c.Login,
			Namespace: "api",
			Name:      "fixture.api.login",
		},
		&router.RouteConfig{
			Path:        "/v1/protected",
			Method:      core.GET,
			Handler:     c.Protected,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.protected",
		},
		&router.RouteConfig{
			Path:        "/v1/widgets",
			Method:      core.GET,
			Handler:     c.ListWidgets,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.widgets.list",
		},
		&router.RouteConfig{
			Path:        "/v1/widgets",
			Method:      core.POST,
			Handler:     c.CreateWidget,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.widgets.create",
		},
		&router.RouteConfig{
			Path:        "/v1/widgets/{id}",
			Method:      core.GET,
			Handler:     c.GetWidget,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.widgets.get",
		},
		&router.RouteConfig{
			Path:        "/v1/widgets/{id}",
			Method:      core.PUT,
			Handler:     c.UpdateWidget,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.widgets.update",
		},
		&router.RouteConfig{
			Path:        "/v1/widgets/{id}/tags",
			Method:      core.PUT,
			Handler:     c.UpdateWidgetTags,
			Middlewares: []core.IMiddleware{apiAuth},
			Namespace:   "api",
			Name:        "fixture.api.widgets.tags.update",
		},
		&router.RouteConfig{
			Path:      "/v1/parse/json",
			Method:    core.POST,
			Handler:   c.JSONEcho,
			Namespace: "api",
			Name:      "fixture.api.parse.json",
		},
		&router.RouteConfig{
			Path:      "/v1/parse/query",
			Method:    core.GET,
			Handler:   c.QueryEcho,
			Namespace: "api",
			Name:      "fixture.api.parse.query",
		},
		&router.RouteConfig{
			Path:      "/v1/parse/upload",
			Method:    core.POST,
			Handler:   c.Upload,
			Namespace: "api",
			Name:      "fixture.api.parse.upload",
		},
	}
}

func NotFound(message core.HttpMessage) {
	if strings.HasPrefix(message.Request().RawRequest().URL.Path, "/api/") {
		message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, "route not found"), http.StatusNotFound)
		return
	}

	message.Response().Text("route not found", http.StatusNotFound)
}

func WriteError(status int, code core.HttpStatus, errPayload any, message core.HttpMessage) {
	message.Response().JSON(dto.ReturnObject(nil, code, errPayload), status)
}

func DecodeRequestBody(body []byte) map[string]any {
	payload := map[string]any{}
	_ = json.Unmarshal(body, &payload)
	return payload
}
