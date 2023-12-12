package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
)

type LangMiddleware struct {
}

func (thiz LangMiddleware) Handle(message core.HttpMessage) bool {
	lang := chi.URLParam(message.GetRequest(), "lang")
	if lang == viper.GetString("i18n.lang.default") {
		defaultLangLen := len(lang) + 1
		url := message.GetRequest().URL.Path[defaultLangLen:]
		message.Redirect(url, 302)
		return false
	}
	return true
}
