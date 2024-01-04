package http

import (
	"context"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/go-chi/chi"
	"net/http"
	"net/url"
)

type MessageContext struct {
	URL           *url.URL
	RequestURI    string
	CookieManager core.ICookieManager
	Headers       http.Header
	Session       core.ISession
	Request       *http.Request
	AuthStrategy  core.IAuthStrategy

	Parent context.Context
}

func (thiz MessageContext) GetURL() *url.URL {
	return thiz.URL
}

func (thiz MessageContext) GetRequestURL() string {
	return thiz.RequestURI
}

func (thiz MessageContext) GetCookieManager() core.ICookieManager {
	return thiz.CookieManager
}

func (thiz MessageContext) GetHeader() http.Header {
	return thiz.Headers
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

func (thiz MessageContext) GetSession() core.ISession {
	return thiz.Session
}

func (thiz MessageContext) GetRequest() *http.Request {
	return thiz.Request
}

func (thiz MessageContext) GetAuthStrategy() core.IAuthStrategy {
	return thiz.AuthStrategy
}
