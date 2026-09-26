package provider

import (
	"net/http"

	"github.com/osbits/gorgany/v2/app/core"
	grgprovider "github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service/dto"
)

// newErrorProvider maps the app's error types to responses. A handler is keyed by
// the error's Go type name without its package; anything unmatched falls through to
// the framework's defaults, which keep internal detail out of prod responses.
func newErrorProvider() *grgprovider.ErrorProvider {
	p := grgprovider.NewErrorProvider()
	p.AddHandler("NotFoundError", notFoundError)
	return p
}

func notFoundError(err error, message core.HttpMessage) {
	message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, err.Error()), http.StatusNotFound)
}
