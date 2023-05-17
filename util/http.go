package util

import (
	"github.com/spf13/viper"
	"regexp"
)

func AddLocaleToURL(locale string, url string) string {
	if locale == viper.GetString("i18n.lang.default") {
		return url
	}
	regex := regexp.MustCompile(`^(https?:\\/\\/)`)
	if !regex.MatchString(url) {
		if url[0] == '/' {
			url = locale + url
		} else {
			url = locale + "/" + url
		}
	}

	if url[len(url)-1] == '/' && len(url) > 1 {
		url = url[:len(url)-1]
	}

	return "/" + url
}
