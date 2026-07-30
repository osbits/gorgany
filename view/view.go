package view

import (
	"context"
	"github.com/go-chi/chi"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/i18n"
	"github.com/osbits/gorgany/v2/service"
	"github.com/osbits/gorgany/v2/util"
	"html/template"
	"io"
)

// EngineRenderer implements the core.IEngineRenderer interface
type EngineRenderer struct {
	Engine            core.IViewEngine          `container:"inject"`
	Router            core.Router               `container:"inject"`
	PaginationService service.PaginationService `container:"inject"`
	functions         map[string]any
	variables         map[string]any
}

func (er *EngineRenderer) Init() {
	er.functions = make(map[string]any)
	er.variables = make(map[string]any)
}

func (er *EngineRenderer) DoRender(ctx context.Context, w io.Writer, templateName string, opts map[string]any) error {
	if opts == nil {
		opts = make(map[string]any)
	}

	lrc := newLocalRenderingContext(ctx, er)
	defer lrc.Close()

	opts = er.registerDefaultOptions(lrc, opts)
	opts = er.registerFunctions(lrc, opts)

	err := er.Engine.Render(w, templateName, opts)
	if err != nil {
		return err
	}

	return nil
}

func (er *EngineRenderer) RegisterGlobalFunction(name string, f any) {
	er.functions[name] = f
}

func (er *EngineRenderer) RegisterGlobalVariable(name string, v any) {
	er.variables[name] = v
}

func (er *EngineRenderer) registerFunctions(lrc *localRenderingContext, opts map[string]any) map[string]any {
	funcs := map[string]any{
		"__":         lrc.__,
		"UrlByName":  lrc.UrlByName,
		"AssetPath":  lrc.AssetPath,
		"PublicPath": lrc.PublicPath,
		"SafeHtml":   lrc.SafeHtml,
		"Pagination": er.PaginationService.Pagination,
	}
	// opts is assembled by callers, so an "fn" that is not a func map is a caller
	// mistake rather than an invariant. Treat it as absent instead of panicking.
	existingFuncs, ok := opts["fn"].(map[string]any)
	if !ok {
		existingFuncs = map[string]any{}
	}
	merged := util.MergeMaps(existingFuncs, er.functions)
	opts["fn"] = util.MergeMaps(funcs, merged)
	return opts
}

// registerDefaultOptions добавляет стандартные переменные.
func (er *EngineRenderer) registerDefaultOptions(lrc *localRenderingContext, opts map[string]any) map[string]any {
	allLocales := i18n.AvailableLocales()
	opts["CurrentLocale"] = lrc.Locale()
	opts["AvailableLocales"] = allLocales
	opts["Ctx"] = lrc.ctx
	return util.MergeMaps(opts, er.variables)
}

type localRenderingContext struct {
	ctx context.Context
	er  *EngineRenderer
}

func newLocalRenderingContext(ctx context.Context, er *EngineRenderer) *localRenderingContext {
	return &localRenderingContext{ctx: ctx, er: er}
}

func (l *localRenderingContext) Close() error { l.ctx = nil; return nil }

func (l *localRenderingContext) __(code string, args ...any) string {
	return i18n.TranslationWithSequence(code, l.Locale(), args...)
}

func (l *localRenderingContext) Locale() string {
	lang := chi.URLParamFromCtx(l.ctx, "lang")
	if lang == "" {
		lang = i18n.DefaultLocale()
	}
	return lang
}

func (l *localRenderingContext) UrlByName(name string, params ...any) string {
	return l.er.Router.UrlByNameSequence(name, params...)
}

func (l *localRenderingContext) AssetPath(path string, absolute bool) template.URL {
	return template.URL(util.AssetPath(path, absolute))
}

func (l *localRenderingContext) PublicPath(path string, absolute bool) template.URL {
	return template.URL(util.PublicPath(path, absolute))
}

func (l *localRenderingContext) SafeHtml(s string) template.HTML { return template.HTML(s) }
