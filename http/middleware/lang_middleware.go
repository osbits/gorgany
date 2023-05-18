package middleware

import (
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/http"
)

type LangMiddleware struct {
}

func (thiz LangMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		lang := chi.URLParam(message.GetRequest(), "lang")
		if lang == viper.GetString("i18n.lang.default") {
			defaultLangLen := len(lang) + 1
			url := message.GetRequest().URL.Path[defaultLangLen:]
			message.Redirect(url, 302)
			return
		}
		next(message)
	}
}
