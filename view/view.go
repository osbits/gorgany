package view

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/i18n"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"html/template"
	"io"
	"os"
	"regexp"
	"strings"
)

func NewEngineRenderer(ctx context.Context) *EngineRenderer {
	return &EngineRenderer{
		Engine: internal.GetFrameworkRegistrar().GetViewEngine(),
	}
}

type EngineRenderer struct {
	Engine core.IViewEngine `container:"inject"`
}

func (thiz EngineRenderer) DoRender(ctx context.Context, w io.Writer, templateName string, opts map[string]any) error {
	if opts == nil {
		opts = make(map[string]any)
	}

	opts = thiz.registerDefaultOptions(ctx, opts)
	opts = thiz.registerFunctions(opts)

	return thiz.Engine.Render(w, templateName, opts)
}

func (thiz EngineRenderer) CreateLink(ctx context.Context, url string, absolute ...bool) string {
	if len(absolute) > 0 {
		return util.CreateLink(url, thiz.Locale(ctx), absolute[0])
	}
	return util.CreateLink(url, thiz.Locale(ctx), false)
}

func (thiz EngineRenderer) CreateLinkWithNamespace(ctx context.Context, url string, namespace string) string {
	return util.AddLocaleToURL(thiz.Locale(ctx), fmt.Sprintf("/%s%s", namespace, url))
}

func (thiz EngineRenderer) __(ctx context.Context, code string, opts ...any) string {
	return i18n.TranslationWithSequence(code, thiz.Locale(ctx), opts)
}

func (thiz EngineRenderer) Locale(ctx context.Context) string {
	locale := chi.URLParamFromCtx(ctx, "lang")
	if locale == "" {
		locale = viper.GetString("i18n.lang.default")
	}
	return locale
}

// return slice of langs exclude current one if i18n is enabled
func (thiz EngineRenderer) AvailableLocalesOnFront(ctx context.Context) []string {
	availableLangsOnFront := make([]string, 0)
	availableLocales := i18n.AvailableLocales()
	for _, lang := range availableLocales {
		if lang == thiz.Locale(ctx) {
			continue
		}
		availableLangsOnFront = append(availableLangsOnFront, lang)
	}
	return availableLangsOnFront
}

func (thiz EngineRenderer) ChangeLanguageLink(ctx context.Context, locale string) string {
	path := ""
	if ctx, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
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

func (thiz EngineRenderer) CurrentUrl(ctx context.Context) string {
	if ctx, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
		return ctx.GetRequestURL()
	}
	return ""
}

func (thiz EngineRenderer) AssetPath(path string, absolute bool) template.URL {
	return template.URL(util.AssetPath(path, absolute))
}

func (thiz EngineRenderer) PublicPath(path string, absolute bool) template.URL {
	return template.URL(util.PublicPath(path, absolute))
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
		"AssetPath":               thiz.AssetPath,
		"PublicPath":              thiz.PublicPath,
	}

	return opts
}

func (thiz EngineRenderer) registerDefaultOptions(ctx context.Context, opts map[string]any) map[string]any {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "Gorgany"
	}

	opts["AppName"] = appName
	opts["CurrentLocale"] = thiz.Locale(ctx)
	opts["AvailableLocales"] = thiz.AvailableLocalesOnFront(ctx)
	opts["AllLocales"] = i18n.AllLocales()
	opts["Ctx"] = ctx

	return opts
}
