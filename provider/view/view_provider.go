package view

import (
	"graecoFramework/view"
)

var Engine *view.AmberEngine

func NewViewProvider() *Provider {
	return &Provider{}
}

type Provider struct {
}

func (thiz Provider) InitProvider() {
	Engine = view.NewAmberEngine("./resource/view", "amber")
}
