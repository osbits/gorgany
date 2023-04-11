package http

import (
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

func (thiz PublicController) load(message Message) {
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

func (thiz PublicController) GetRoutes() []*RouteConfig {
	return []*RouteConfig{
		{
			Path:    "/public/*",
			Method:  GET,
			Handler: thiz.load,
		},
	}
}
