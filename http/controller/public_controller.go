package controller

import (
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http/router"
)

func NewPublicController() *PublicController {
	return &PublicController{}
}

type PublicController struct {
}

func (thiz PublicController) load(message core.HttpMessage) {
	r := message.Request().RawRequest()
	url := r.URL
	path := url.Path

	// Clean and validate the path
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "..") {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Ensure path starts with /public/
	if !strings.HasPrefix(cleanPath, "/public/") {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Remove /public/ prefix to get the actual file path
	filePath := strings.TrimPrefix(cleanPath, "/public/")

	// Join with resource directory
	fullPath := filepath.Join("resource", filePath)

	// Verify the file exists and is within the resource directory
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		message.Response().Text("Internal server error", 500)
		return
	}

	absResource, err := filepath.Abs("resource")
	if err != nil {
		message.Response().Text("Internal server error", 500)
		return
	}

	if !strings.HasPrefix(absPath, absResource) {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Read the file
	file, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			message.Response().Text("File not found", 404)
		} else {
			message.Response().Text("Internal server error", 500)
		}
		return
	}

	// Get file extension and mime type
	ext := filepath.Ext(filePath)
	if ext == "" {
		message.Response().Text("Invalid file type", 400)
		return
	}

	kind := mime.TypeByExtension(ext)
	if kind == "" {
		kind = "application/octet-stream"
	}

	// Set content type and send response
	message.Response().SetHeader("Content-Type", kind)
	message.Response().Bytes(file, 200)
}

func (thiz PublicController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:    "/public/*",
			Method:  core.GET,
			Handler: thiz.load,
		},
	}
}
