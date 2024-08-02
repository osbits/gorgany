package auth

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/auth"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/util"
	"net/http"
	"net/url"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct{}

func (thiz LoginController) ShowLogin(message core.HttpMessage) {
	appCtx := message.Context().Value(core.ApplicationContextKey).(core.IApplicationContext)
	homeUrl := appCtx.GetHomeUrl()

	if auth.Strategy().IsLoggedIn(message.Context()) {
		message.Redirect(homeUrl, 301)
	}

	message.Render("auth/login", nil)
}

func (thiz LoginController) Login(message core.HttpMessage) {
	appCtx := message.Context().Value(core.ApplicationContextKey).(core.IApplicationContext)
	homeUrl := appCtx.GetHomeUrl()

	if auth.Strategy().IsLoggedIn(message.Context()) {
		message.Redirect(homeUrl, 301)
	}

	body := message.GetBodyContent()
	values, _ := url.ParseQuery(body)
	username := values.Get("username")
	password := values.Get("password")
	user, err := auth.GetAuthEntityService().GetByUsername(username)
	if err != nil {
		err2.HandleError(err)
		message.RedirectWithParams(router.GetRouter().UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), password) {
		message.RedirectWithParams(router.GetRouter().UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": "We were unable to find a user with the specified email address and password"})
		return
	}

	_, err = auth.Strategy().Login(user, message.Context())
	if err != nil {
		err2.HandleError(err)
		message.RedirectWithParams(router.GetRouter().UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	message.Redirect(homeUrl, 301)
}

func (thiz LoginController) Logout(message core.HttpMessage) {
	auth.Strategy().Logout(message.Context())
	message.Redirect(router.GetRouter().UrlByNameSequence("cp.login.show"), http.StatusTemporaryRedirect)
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
