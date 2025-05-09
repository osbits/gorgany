package http

import (
	"context"
	"net/http"
	"net/url"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/go-chi/chi"
)

type messageContext struct {
	url           *url.URL
	requestURI    string
	cookieManager core.ICookieManager
	headers       http.Header
	session       core.ISession
	request       *http.Request

	requestId string
	ip        string

	requestCtx context.Context
}

func (thiz *messageContext) GetURL() *url.URL {
	return thiz.url
}

func (thiz *messageContext) GetRequestURL() string {
	return thiz.requestURI
}

func (thiz *messageContext) GetCookieManager() core.ICookieManager {
	return thiz.cookieManager
}

func (thiz *messageContext) GetHeader() http.Header {
	return thiz.headers
}

func (thiz *messageContext) GetBearerToken() string {
	bearerToken := thiz.GetHeader().Get("Authorization")
	return util.ParseBearerToken(bearerToken)
}

func (thiz *messageContext) GetPathParam(name string) string {
	return chi.URLParamFromCtx(thiz.requestCtx, name)
}

func (thiz *messageContext) GetSession() core.ISession {
	return thiz.session
}

func (thiz *messageContext) GetRequest() *http.Request {
	return thiz.request
}

func (thiz *messageContext) GetRequestContext() context.Context {
	return thiz.requestCtx
}

func (thiz *messageContext) GetRequestId() string {
	return thiz.requestId
}

func (thiz *messageContext) GetIp() string {
	return thiz.ip
}
