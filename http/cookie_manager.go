package http

import (
	"net/http"
)

type CookieManager struct {
	writer  http.ResponseWriter
	request *http.Request
}

func NewCookieManager(writer http.ResponseWriter, r *http.Request) *CookieManager {
	return &CookieManager{
		writer:  writer,
		request: r,
	}
}

func (thiz *CookieManager) SetCookie(cookie *http.Cookie) {
	http.SetCookie(thiz.writer, cookie)
}

func (thiz *CookieManager) GetCookie(key string) *http.Cookie {
	cookie, err := thiz.request.Cookie(key)
	if err != nil {
		return nil
	}
	return cookie
}

func (thiz *CookieManager) GetCookies() []*http.Cookie {
	return thiz.request.Cookies()
}
