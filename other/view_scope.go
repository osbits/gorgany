package http

import (
	"context"
	"github.com/osbits/gorgany/app/core"
	"net/http"
)

type HTTPViewScope struct {
	Engine core.IViewEngine `container:"inject"`
}

// NewHTTPViewScope constructs a HTTPViewScope.
func NewHTTPViewScope() *HTTPViewScope {
	return &HTTPViewScope{}
}

// Render executes a template with data and writes to ResponseWriter.
func (vc *HTTPViewScope) Render(ctx context.Context, w http.ResponseWriter, tpl string, data map[string]any) {
	if err := vc.Engine.Render(w, tpl, data); err != nil {
		panic(err)
	}
}
