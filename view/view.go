package view

import (
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/http/router"
	"gorgany/i18n"
	"gorgany/util"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
)

var engine Engine

func SetEngine(e Engine) {
	engine = e
}

func GetEngine() Engine {
	return engine
}

// RequestWrapper
func NewRequestWrapper(request *http.Request) *requestWrapper {
	return &requestWrapper{request: request}
}

type requestWrapper struct {
	request *http.Request
}

// Engine
type Engine interface {
	Render(w io.Writer, templateName string, opts map[string]any) error
}

func NewEngineRenderer(requestWrapper *requestWrapper) *EngineRenderer {
	return &EngineRenderer{
		Engine:         engine,
		requestWrapper: requestWrapper,
	}
}

type EngineRenderer struct {
	Engine         Engine
	requestWrapper *requestWrapper
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
	locale := chi.URLParam(thiz.requestWrapper.request, "lang")
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
	path := thiz.requestWrapper.request.URL.Path

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
	return thiz.requestWrapper.request.RequestURI
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
