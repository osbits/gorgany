package core

import (
	"context"
	"io"
)

// IEngineRenderer defines the interface for the engine renderer
type IEngineRenderer interface {
	// DoRender renders a template with the given options
	DoRender(ctx context.Context, w io.Writer, templateName string, opts map[string]any) error

	// RegisterGlobalFunction registers a global function for templates
	RegisterGlobalFunction(name string, f any)

	// RegisterGlobalVariable registers a global variable for templates
	RegisterGlobalVariable(name string, v any)
}

type IViewEngine interface {
	Render(w io.Writer, templateName string, opts map[string]any) error
}
