package auth

import (
	"fmt"
	"gorgany/app/core"
	"gorgany/auth"
	"gorgany/http/router"
	"gorgany/internal"
	"gorgany/util"
	"net/url"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct{}

func (thiz LoginController) ShowLogin(message core.HttpMessage) {
	if message.IsLoggedIn() {
		message.Redirect(internal.GetFrameworkRegistrar().GetHomeUrl(), 301)
	}

	message.Render("auth/login", nil)
}

func (thiz LoginController) Login(message core.HttpMessage) {
	if message.IsLoggedIn() {
		message.Redirect(internal.GetFrameworkRegistrar().GetHomeUrl(), 301)
	}

	body := message.GetBodyContent()
	values, _ := url.ParseQuery(body)
	username := values.Get("username")
	password := values.Get("password")
	user, err := auth.GetAuthEntityService().GetByUsername(username)
	if err != nil {
		message.RedirectWithParams(router.GetRouter().UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), password) {
		message.RedirectWithParams(router.GetRouter().UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": "We were unable to find a user with the specified email address and password"})
		return
	}

	message.Login(user)

	message.Redirect(internal.GetFrameworkRegistrar().GetHomeUrl(), 301)
}

func (thiz LoginController) Logout(message core.HttpMessage) {
	message.Logout()
	message.Redirect(router.GetRouter().UrlByNameSequence("cp.login.show"), 301)
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
