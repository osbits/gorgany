package auth

import (
	"fmt"
	"github.com/osbits/gorgany/app/core"
	err2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/http/router"
	"github.com/osbits/gorgany/util"
	"net/http"
	"net/url"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct {
	webContext  core.IWebContext  `container:"inject"`
	authContext core.IAuthContext `container:"inject"`
	userService core.IUserService `container:"inject"`
	router      core.Router       `container:"inject"`
}

func (thiz LoginController) ShowLogin(message core.HttpMessage) {
	homeUrl := thiz.webContext.GetHomeUrl()

	if thiz.authContext.ResolveAuthStrategyByContext(message.Context()).IsLoggedIn(message.Context()) {
		message.Response().Redirect(homeUrl, 301)
	}

	message.View().Render("auth/login", nil)
}

func (thiz LoginController) Login(message core.HttpMessage) {
	homeUrl := thiz.webContext.GetHomeUrl()

	if thiz.authContext.ResolveAuthStrategyByContext(message.Context()).IsLoggedIn(message.Context()) {
		message.Response().Redirect(homeUrl, 301)
	}

	body, _ := message.Request().Body()
	values, _ := url.ParseQuery(string(body))
	username := values.Get("username")
	password := values.Get("password")
	user, err := thiz.userService.GetByUsername(username)
	if err != nil {
		err2.HandleError(err)
		message.RedirectWithFlash(thiz.router.UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), password) {
		message.RedirectWithFlash(thiz.router.UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": "We were unable to find a user with the specified email address and password"})
		return
	}

	_, err = thiz.authContext.ResolveAuthStrategyByContext(message.Context()).Login(user, message.Context())
	if err != nil {
		err2.HandleError(err)
		message.RedirectWithFlash(thiz.router.UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	message.Response().Redirect(homeUrl, 301)
}

func (thiz LoginController) Logout(message core.HttpMessage) {
	thiz.authContext.ResolveAuthStrategyByContext(message.Context()).Logout(message.Context())
	message.Response().Redirect(thiz.router.UrlByNameSequence("cp.login.show"), http.StatusTemporaryRedirect)
}

func (thiz LoginController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:    "/login",
			Method:  core.GET,
			Handler: thiz.ShowLogin,
			Name:    "cp.login.show",
		},
		&router.RouteConfig{
			Path:    "/login",
			Method:  core.POST,
			Handler: thiz.Login,
			Name:    "cp.login",
		},
		&router.RouteConfig{
			Path:    "/logout",
			Method:  core.POST,
			Handler: thiz.Logout,
			Name:    "cp.logout",
		},
	}
}
