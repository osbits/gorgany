package core

import "io"

type IViewEngine interface {
	Render(w io.Writer, templateName string, opts map[string]any) error
}
