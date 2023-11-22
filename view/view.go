package view

import (
	"context"
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/app/core"
	"gorgany/http/router"
	"gorgany/i18n"
	"gorgany/internal"
	"gorgany/util"
	"io"
	"os"
	"regexp"
	"strings"
)

func NewEngineRenderer(ctx context.Context) *EngineRenderer {
	return &EngineRenderer{
		Engine: internal.GetFrameworkRegistrar().GetViewEngine(),
		Ctx:    ctx,
	}
}

type EngineRenderer struct {
	Engine core.IViewEngine `container:"inject"`
	Ctx    context.Context
}

func (thiz *EngineRenderer) Init() {
	if thiz.Ctx == nil {
		thiz.Ctx = context.Background()
	}
}

func (thiz EngineRenderer) DoRender(w io.Writer, templateName string, opts map[string]any) error {
	if opts == nil {
		opts = make(map[string]any)
	}

	opts = thiz.registerDefaultOptions(opts)
	opts = thiz.registerFunctions(opts)

	return thiz.Engine.Render(w, templateName, opts)
}

func (thiz EngineRenderer) CreateLink(url string) string {
	return util.AddLocaleToURL(thiz.Locale(), url)
}

func (thiz EngineRenderer) CreateLinkWithNamespace(url string, namespace string) string {
	return util.AddLocaleToURL(thiz.Locale(), fmt.Sprintf("/%s%s", namespace, url))
}

func (thiz EngineRenderer) __(code string, opts ...any) string {
	return i18n.TranslationWithSequence(code, thiz.Locale(), opts)
}

func (thiz EngineRenderer) Locale() string {
	locale := chi.URLParamFromCtx(thiz.Ctx, "lang")
	if locale == "" {
		locale = viper.GetString("i18n.lang.default")
	}
	return locale
}

// return slice of langs exclude current one if i18n is enabled
func (thiz EngineRenderer) AvailableLocalesOnFront() []string {
	availableLangsOnFront := make([]string, 0)
	availableLocales := i18n.AvailableLocales()
	for _, lang := range availableLocales {
		if lang == thiz.Locale() {
			continue
		}
		availableLangsOnFront = append(availableLangsOnFront, lang)
	}
	return availableLangsOnFront
}

func (thiz EngineRenderer) ChangeLanguageLink(locale string) string {
	path := ""
	if ctx, ok := thiz.Ctx.Value(core.MessageURLContextKey).(core.IMessageContext); ok {
		path = ctx.GetURL().Path
	}

	availableLangs := viper.GetStringSlice("i18n.lang.available")
	availableLangs = append(availableLangs, viper.GetString("i18n.lang.default"))

	regex := regexp.MustCompile(fmt.Sprintf("^/(?P<lang>%s)", strings.Join(availableLangs, "|")))

	processedPath := regex.ReplaceAllStringFunc(path, func(pattern string) string {
		foundStrings := regex.FindStringSubmatch(pattern)
		if len(foundStrings) != 2 {
			return path
		}
		return "/" + locale
	})

	if processedPath == path {
		processedPath = util.AddLocaleToURL(locale, path)
	}

	return processedPath
}

func (thiz EngineRenderer) CurrentUrl() string {
	if ctx, ok := thiz.Ctx.Value(core.MessageURLContextKey).(core.IMessageContext); ok {
		return ctx.GetRequestURL()
	}
	return ""
}

func (thiz EngineRenderer) registerFunctions(opts map[string]any) map[string]any {
	opts["fn"] = map[string]any{
		"InArray":                 util.InArray,
		"Pluck":                   util.Pluck,
		"CreateLink":              thiz.CreateLink,
		"__":                      thiz.__,
		"ChangeLanguageLink":      thiz.ChangeLanguageLink,
		"CurrentUrl":              thiz.CurrentUrl,
		"CreateLinkWithNamespace": thiz.CreateLinkWithNamespace,
		"UrlByName":               router.GetRouter().UrlByNameSequence,
	}

	return opts
}

func (thiz EngineRenderer) registerDefaultOptions(opts map[string]any) map[string]any {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "Gorgany"
	}

	opts["AppName"] = appName
	opts["CurrentLocale"] = thiz.Locale()
	opts["AvailableLocales"] = thiz.AvailableLocalesOnFront()
	opts["AllLocales"] = i18n.AllLocales()

	return opts
}
