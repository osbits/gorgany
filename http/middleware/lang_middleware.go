package middleware

import (
	"github.com/go-chi/chi"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/spf13/viper"
)

type LangMiddleware struct {
}

func (thiz LangMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		lang := chi.URLParam(message.Request().RawRequest(), "lang")
		if lang == viper.GetString("i18n.lang.default") {
			defaultLangLen := len(lang) + 1
			url := message.Request().RawRequest().URL.Path[defaultLangLen:]
			message.Response().Redirect(url, 302)
			return
		}
		next(message)
	}
}
