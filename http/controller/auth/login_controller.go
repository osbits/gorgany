package auth

import (
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	err2 "github.com/osbits/gorgany/v2/err"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/util"
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

// ShowLogin renders the login form, unless the visitor is already authenticated.
//
// The redirect belongs here and only here. A GET carries no credentials, so there is nothing to
// verify and nothing to decide: somebody who is already signed in and asks for the login form
// belongs on the home page. The return matters as much as the redirect — this used to render the
// form into a response that already carried the 301, so the login page was served as the body of
// a redirect away from it.
func (thiz LoginController) ShowLogin(message core.HttpMessage) {
	homeUrl := thiz.webContext.GetHomeUrl()

	if thiz.authContext.ResolveAuthStrategyByContext(message.Context()).IsLoggedIn(message.Context()) {
		message.Response().Redirect(homeUrl, 301)
		return
	}

	message.View().Render("auth/login", nil)
}

// Login authenticates the credentials this request carries. Deliberately, whatever session it
// arrives on.
//
// There used to be a guard here that answered an authenticated request with a redirect home, and
// it is worth spelling out why it had to go, because it looks like the careful thing to do. A
// login POST that is answered with a redirect is a login POST whose credentials were never
// compared to anything, so whoever the session already belonged to keeps it and the poster is
// handed that identity. Turn that around and it is an attack: a party who can write the session
// cookie into somebody else's browser plants their own *signed-in* session instead of a blank
// one. The victim's login bounces off the guard, their password is never checked, and they spend
// the visit inside the planted account — typing into it, acting in it — while the party who
// planted it holds a live cookie for the same session. Nothing about the traffic looks unusual.
//
// Authenticating over a live session is safe here because the strategy's Login replaces the
// session identifier rather than assigning a user id to the one that arrived: it revokes what
// was presented and mints a fresh identifier with a fresh CSRF token. That is also what closed
// the hazard the guard was added for — two different valid user ids reaching one session object
// cannot happen when the session is replaced instead of reused.
//
// A failed check leaves the session that was already there untouched. The alternative, ending it
// whenever an attempt on it fails, reads as the safer default and is not: it would give anyone
// who can get a browser to post this form a remote logout, no credentials required, whereas a
// caller who mistyped their own password loses nothing by typing it again.
func (thiz LoginController) Login(message core.HttpMessage) {
	homeUrl := thiz.webContext.GetHomeUrl()

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

	session, err := thiz.authContext.ResolveAuthStrategyByContext(message.Context()).Login(user, message.Context())
	if err != nil {
		err2.HandleError(err)
		message.RedirectWithFlash(thiz.router.UrlByNameSequence("cp.login.show"), 301, map[string]any{"error": fmt.Sprintf("Unexpected error during find user %s in our storage", username)})
		return
	}

	// Authenticating rotates the session identifier, so the session this request resolved on
	// its way in has been deleted. Everything that asks about the session from here on — this
	// handler, any middleware still to run, IsLoggedIn on the redirect — has to be told which
	// session is current, or a login that succeeded looks like one that failed.
	grghttp.PublishSession(message, session)

	message.Response().Redirect(homeUrl, 301)
}

// Logout sends the user back to the login form, unless the session could not be revoked.
//
// Reporting the failure matters more than it looks. A logout that cannot delete the
// server-side session leaves it usable, and the strategy deliberately keeps the cookie in
// place in that case, so redirecting to the login page as if nothing had happened would tell
// the user they are logged out of a session that is still live — the outcome logout exists
// to prevent. The flash names the failure instead, and the user can retry.
func (thiz LoginController) Logout(message core.HttpMessage) {
	loginUrl := thiz.router.UrlByNameSequence("cp.login.show")

	if err := thiz.authContext.ResolveAuthStrategyByContext(message.Context()).
		Logout(message.Context()); err != nil {
		err2.HandleError(err)
		message.RedirectWithFlash(loginUrl, http.StatusTemporaryRedirect,
			map[string]any{"error": "We could not end your session. You are still signed in; " +
				"please try again."})
		return
	}

	message.Response().Redirect(loginUrl, http.StatusTemporaryRedirect)
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
