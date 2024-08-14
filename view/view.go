package view

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/i18n"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/service"
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
		Engine: internal.GetApplicationContext().GetViewEngine(),
	}
}

type EngineRenderer struct {
	Engine    core.IViewEngine `container:"inject"`
	functions map[string]any
	variables map[string]any
}

func (thiz *EngineRenderer) Init() {
	thiz.functions = make(map[string]any)
	thiz.variables = make(map[string]any)
}

func (thiz *EngineRenderer) DoRender(ctx context.Context, w io.Writer, templateName string, opts map[string]any) error {
	if opts == nil {
		opts = make(map[string]any)
	}

	lrc := &localRenderingContext{ctx: ctx}
	defer lrc.Close()

	opts = thiz.registerDefaultOptions(lrc, opts)
	opts = thiz.registerFunctions(lrc, opts)

	return thiz.Engine.Render(w, templateName, opts)
}

func (thiz *EngineRenderer) RegisterGlobalFunction(name string, f any) {
	thiz.functions[name] = f
}

func (thiz *EngineRenderer) RegisterGlobalVariable(name string, v any) {
	thiz.variables[name] = v
}

func (thiz *EngineRenderer) registerFunctions(localRenderingContext *localRenderingContext, opts map[string]any) map[string]any {
	opts["fn"] = map[string]any{
		"__": localRenderingContext.__,

		"CreateLink":              localRenderingContext.CreateLink,
		"ChangeLanguageLink":      localRenderingContext.ChangeLanguageLink,
		"CurrentUrl":              localRenderingContext.CurrentUrl,
		"CreateLinkWithNamespace": localRenderingContext.CreateLinkWithNamespace,

		"AssetPath":  localRenderingContext.AssetPath,
		"PublicPath": localRenderingContext.PublicPath,

		"SafeHtml":   localRenderingContext.SafeHtml,
		"Pagination": localRenderingContext.Pagination,

		"InArray": util.InArray,
		"Pluck":   util.Pluck,

		"UrlByName": router.GetRouter().UrlByNameSequence,
	}

	return util.MergeMaps(opts, thiz.functions)
}

func (thiz *EngineRenderer) registerDefaultOptions(localRenderingContext *localRenderingContext, opts map[string]any) map[string]any {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "Gorgany"
	}

	opts["AppName"] = appName
	opts["CurrentLocale"] = localRenderingContext.Locale()
	opts["AvailableLocales"] = localRenderingContext.AvailableLocalesOnFront()
	opts["AllLocales"] = i18n.AllLocales()
	opts["Ctx"] = localRenderingContext.ctx

	return util.MergeMaps(opts, thiz.variables)
}

type localRenderingContext struct {
	ctx context.Context
}

func (thiz *localRenderingContext) CreateLink(url string, absolute ...bool) string {
	if len(absolute) > 0 {
		return util.CreateLink(url, thiz.Locale(), absolute[0])
	}
	return util.CreateLink(url, thiz.Locale(), false)
}

func (thiz *localRenderingContext) CreateLinkWithNamespace(url string, namespace string) string {
	return util.AddLocaleToURL(thiz.Locale(), fmt.Sprintf("/%s%s", namespace, url))
}

func (thiz *localRenderingContext) __(code string, opts ...any) string {
	return i18n.TranslationWithSequence(code, thiz.Locale(), opts...)
}

func (thiz *localRenderingContext) Locale() string {
	locale := chi.URLParamFromCtx(thiz.ctx, "lang")
	if locale == "" {
		locale = viper.GetString("i18n.lang.default")
	}
	return locale
}

// AvailableLocalesOnFront returns slice of langs exclude current one if i18n is enabled
func (thiz *localRenderingContext) AvailableLocalesOnFront() []string {
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

func (thiz *localRenderingContext) ChangeLanguageLink(locale string) string {
	path := ""
	if ctx, ok := thiz.ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
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

func (thiz *localRenderingContext) CurrentUrl() string {
	if ctx, ok := thiz.ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
		return ctx.GetRequestURL()
	}
	return ""
}

func (thiz *localRenderingContext) AssetPath(path string, absolute bool) template.URL {
	return template.URL(util.AssetPath(path, absolute))
}

func (thiz *localRenderingContext) PublicPath(path string, absolute bool) template.URL {
	return template.URL(util.PublicPath(path, absolute))
}

func (thiz *localRenderingContext) SafeHtml(content string) template.HTML {
	return template.HTML(content)
}

func (thiz *localRenderingContext) Pagination(offset, limit, total int) string {
	return service.PaginationService{}.Pagination(thiz.ctx, offset, limit, total)
}

func (thiz *localRenderingContext) Close() error {
	thiz.ctx = nil
	return nil
}
