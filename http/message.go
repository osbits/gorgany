package http

import (
	"fmt"
	"graecoFramework/provider/view"
	"io"
	"net/http"
)

type Message struct {
	writer  http.ResponseWriter
	request *http.Request
}

func (thiz Message) GetRequest() *http.Request {
	return thiz.request
}

func (thiz Message) GetWriter() http.ResponseWriter {
	return thiz.writer
}

// GetBody returns body in bytes
func (thiz Message) GetBody() []byte {
	bodyCloser := thiz.request.Body
	body, err := io.ReadAll(bodyCloser)
	if err != nil {
		panic(fmt.Errorf("Error during read body from request, %v", err))
	}
	return body
}

// GetBodyContent returns body in string
func (thiz Message) GetBodyContent() string {
	body := thiz.GetBody()
	return string(body)
}

func (thiz Message) GetHeader() http.Header {
	return thiz.request.Header
}

func (thiz Message) GetCookies() []*http.Cookie {
	return thiz.request.Cookies()
}

func (thiz Message) Render(template string, options any) {
	err := view.Engine.Render(thiz.writer, template, options)
	if err != nil {
		panic(fmt.Errorf("Error during render template '%s', %v", template, err))
	}
}

func (thiz Message) ResponseHeader() http.Header {
	return thiz.writer.Header()
}

func (thiz Message) Response(responseBody string, statusCode int) {
	_, err := thiz.writer.Write([]byte(responseBody))
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", responseBody, err))
	}
	thiz.writer.WriteHeader(statusCode)
}

func (thiz Message) ResponseBytes(responseBody []byte, statusCode int) {
	thiz.writer.WriteHeader(statusCode)
	_, err := thiz.writer.Write(responseBody)
	if err != nil {
		thiz.writer.WriteHeader(500)
		panic(fmt.Errorf("Error during response body: %s, %v", string(responseBody), err))
	}
}
