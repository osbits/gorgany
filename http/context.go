package http

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/osbits/gorgany/v2/app/core"
)

type messageContext struct {
	url           *url.URL
	requestURI    string
	cookieManager core.ICookieManager
	headers       http.Header
	request       *http.Request

	requestId string
	ip        string

	requestCtx context.Context

	// mu guards the two fields below, which are the only ones that change after Init.
	//
	// The session does change: Message.Context writes the session scope's current session
	// onto this object every time it is called, which is how a session installed
	// mid-request reaches a context.Context a handler captured before the swap (see
	// PublishSession). A handler that fans out has several goroutines calling Context and
	// reading GetSession, and the reader is the authentication strategy resolving the
	// principal - so this is not a field that can be left unsynchronised.
	mu      sync.Mutex
	session core.ISession

	// The identity resolved from this request, memoised for its lifetime. See
	// core.IRequestIdentityMemo: it is held here, on the request, rather than on the
	// access control object every request shares.
	identityKey    string
	identity       any
	identityStored bool
}

var (
	_ core.IMessageContext      = (*messageContext)(nil)
	_ core.IRequestIdentityMemo = (*messageContext)(nil)
)

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

func (thiz *messageContext) GetPathParam(name string) string {
	return chi.URLParamFromCtx(thiz.requestCtx, name)
}

func (thiz *messageContext) GetSession() core.ISession {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.session
}

// setSession installs the session the rest of the request sees. Called from
// Message.Context, and through it from PublishSession.
func (thiz *messageContext) setSession(session core.ISession) {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	thiz.session = session
}

// LoadIdentity, StoreIdentity and InvalidateIdentity implement core.IRequestIdentityMemo.
//
// The slot holds one identity at a time, which is all a request has. It is handed back only
// for the key it was stored under, so a request whose principal changed - a login rotates
// the session onto a new identifier and republishes it - misses and resolves again instead
// of being answered about who it used to be.
func (thiz *messageContext) LoadIdentity(key string) (any, bool) {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	if !thiz.identityStored || thiz.identityKey != key {
		return nil, false
	}
	return thiz.identity, true
}

func (thiz *messageContext) StoreIdentity(key string, identity any) {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	thiz.identityKey = key
	thiz.identity = identity
	thiz.identityStored = true
}

func (thiz *messageContext) InvalidateIdentity() {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	thiz.identityKey = ""
	thiz.identity = nil
	thiz.identityStored = false
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
