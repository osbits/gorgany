package http

import (
	"context"
	"github.com/go-chi/chi"
	"gorgany/app/core"
	"gorgany/util"
	"net/http"
	"net/url"
)

type MessageContext struct {
	URL        *url.URL
	RequestURI string
	Cookies    []*http.Cookie
	Headers    http.Header

	Parent context.Context
}

func (thiz MessageContext) GetURL() *url.URL {
	return thiz.URL
}

func (thiz MessageContext) GetRequestURL() string {
	return thiz.RequestURI
}

func (thiz MessageContext) GetCookies() []*http.Cookie {
	return thiz.Cookies
}

func (thiz MessageContext) GetCookie(name string) *http.Cookie {
	for i := range thiz.Cookies {
		if thiz.Cookies[i].Name == name {
			return thiz.Cookies[i]
		}
	}

	return nil
}

func (thiz MessageContext) GetHeader() http.Header {
	return thiz.Headers
}

func (thiz MessageContext) GetSessionToken() string {
	sessionTokenCookie := thiz.GetCookie(core.SessionCookieName)
	if sessionTokenCookie == nil {
		return ""
	}

	return sessionTokenCookie.Value
}

func (thiz MessageContext) GetBearerToken() string {
	bearerToken := thiz.GetHeader().Get("Authorization")
	return util.ParseBearerToken(bearerToken)
}

func (thiz MessageContext) GetPathParam(name string) string {
	return chi.URLParamFromCtx(thiz.Parent, name)
}

func (thiz MessageContext) GetParent() context.Context {
	return thiz.Parent
}
