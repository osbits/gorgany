package middleware

import (
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/proxy"
)

type LangMiddleware struct {
}

func (thiz LangMiddleware) Handle(message proxy.HttpMessage) bool {
	lang := chi.URLParam(message.GetRequest(), "lang")
	if lang == viper.GetString("i18n.lang.default") {
		defaultLangLen := len(lang) + 1
		url := message.GetRequest().URL.Path[defaultLangLen:]
		message.Redirect(url, 302)
		return false
	}
	return true
}
