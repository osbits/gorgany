package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http"
)

type ErrorProvider struct {
	handlers map[string]core.ErrorHandler
}

func NewErrorProvider() *ErrorProvider {
	return &ErrorProvider{handlers: make(map[string]core.ErrorHandler)}
}

func (p *ErrorProvider) AddHandler(errName string, handler core.ErrorHandler) {
	p.handlers[errName] = handler
}

func (p *ErrorProvider) Register(c core.IContainer) {}

func (p *ErrorProvider) Boot(c core.IContainer) {
	http.SetErrorHandlers(p.handlers)
}
