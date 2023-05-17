package view

import (
	"context"
	"fmt"
	"gorgany/i18n"
	"gorgany/util"
	"io"
	"os"
)

var engine Engine

func SetEngine(e Engine) {
	engine = e
}

func GetEngine() Engine {
	return engine
}

type Engine interface {
	Render(w io.Writer, templateName string, opts map[string]any) error
}

func NewEngineRenderer(ctx context.Context) *EngineRenderer {
	return &EngineRenderer{
		Engine:  engine,
		Context: ctx,
	}
}

type EngineRenderer struct {
	Engine  Engine
	Context context.Context
}

func (thiz EngineRenderer) DoRender(w io.Writer, templateName string, opts map[string]any) error {
	if opts == nil {
		opts = make(map[string]any)
	}

	opts = thiz.registerDefaultOptions(opts)
	opts = thiz.registerFunctions(opts)

	return thiz.Engine.Render(w, templateName, opts)
}

func (thiz EngineRenderer) registerFunctions(opts map[string]any) map[string]any {
	opts["fn"] = map[string]any{
		"InArray":    util.InArray,
		"Pluck":      util.Pluck,
		"CreateLink": thiz.CreateLink,
		"__":         thiz.__,
	}

	return opts
}

func (thiz EngineRenderer) registerDefaultOptions(opts map[string]any) map[string]any {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "Gograeco"
	}

	opts["AppName"] = appName

	return opts
}

func (thiz EngineRenderer) CreateLink(url string) string {
	fmt.Println(thiz.Locale())
	return util.AddLocaleToURL(thiz.Locale(), url)
}

func (thiz EngineRenderer) __(code string, opts map[string]any) string {
	return i18n.Translation(code, opts, thiz.Locale())
}

func (thiz EngineRenderer) Locale() string {
	locale := thiz.Context.Value("locale")
	if locale == nil {
		panic("Context has no Locale")
	}
	return locale.(string)
}
