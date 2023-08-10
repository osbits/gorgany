package controller

import (
	"gorgany/http"
	"gorgany/http/router"
	"gorgany/proxy"
	"mime"
	"os"
	path2 "path"
	"strings"
)

func NewPublicController() *PublicController {
	return &PublicController{}
}

type PublicController struct {
}

func (thiz PublicController) load(message http.Message) {
	r := message.GetRequest()
	url := r.URL
	path := url.Path
	file, err := os.ReadFile(path2.Join("resource", path))
	if err != nil {
		message.Response("", 404)
		return
	}

	splittedPath := strings.Split(path, "/")
	fileName := splittedPath[len(splittedPath)-1]
	splittedName := strings.Split(fileName, ".")
	ext := splittedName[len(splittedName)-1]
	kind := mime.TypeByExtension("." + ext)
	message.ResponseHeader().Set("content-type", kind)
	message.ResponseBytes(file, 200)
}

func (thiz PublicController) GetRoutes() []proxy.IRouteConfig {
	return []proxy.IRouteConfig{
		&router.RouteConfig{
			Path:    "/public/*",
			Method:  proxy.GET,
			Handler: thiz.load,
		},
	}
}
