package middleware

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany/http"
)

type LangMiddleware struct {
}

func (thiz LangMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		if message.Locale() == viper.GetString("i18n.lang.default") {
			fmt.Println(message.GetRequest().URL)
			return
		}
		next(message)
	}
}
