package util

import (
	"github.com/spf13/viper"
	"regexp"
	"strings"
)

func CreateLink(url string, locale string, absolute bool) string {
	link := AddLocaleToURL(locale, url)
	if absolute {
		if link[0] == '/' {
			return viper.GetString("app.server.url") + link
		}
		return viper.GetString("app.server.url") + "/" + link
	}
	return link
}

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

	if url[0] == '/' {
		return url
	}

	return "/" + url
}

func ParseBearerToken(bearerToken string) string {
	if bearerToken == "" {
		return ""
	}
	token := strings.Split(bearerToken, " ")
	if len(token) < 2 {
		return ""
	}

	return token[1]
}
