package http

import (
	"context"
	"gorgany/app/core"
	"net/url"
)

func NewMessageContext(message Message) context.Context {
	valContext := MessageContext{}
	valContext.URL = message.GetRequest().URL
	valContext.RequestURI = message.GetRequest().RequestURI
	return context.WithValue(message.GetRequest().Context(), core.ContextKey, valContext)
}

type MessageContext struct {
	URL        *url.URL
	RequestURI string
}

func (thiz MessageContext) GetURL() *url.URL {
	return thiz.URL
}

func (thiz MessageContext) GetRequestURL() string {
	return thiz.RequestURI
}
