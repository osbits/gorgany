package view

import (
	"gorgany/view"
)

func NewViewProvider() *Provider {
	return &Provider{}
}

type Provider struct {
}

func (thiz Provider) InitProvider() {
	view.SetEngine(view.NewNativeEngine("./resource/view", "html"))
}
