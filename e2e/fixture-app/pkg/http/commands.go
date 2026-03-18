package fixturehttp

import "github.com/osbits/gorgany/app/core"

type SessionLoginRequest struct {
	Username string `scheme:"username" validate:"required"`
	Password string `scheme:"password" validate:"required"`
}

func (SessionLoginRequest) ContentType() core.ContentType {
	return core.Query
}

type JwtLoginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

func (JwtLoginRequest) ContentType() core.ContentType {
	return core.ApplicationJson
}

type WidgetPayload struct {
	Name        string `json:"name" validate:"required,min=3"`
	Description string `json:"description" validate:"required,min=5"`
}

func (WidgetPayload) ContentType() core.ContentType {
	return core.ApplicationJson
}

type WidgetRelationsPayload struct {
	TagIDs []string `json:"tagIds"`
}

func (WidgetRelationsPayload) ContentType() core.ContentType {
	return core.ApplicationJson
}

type JSONEchoRequest struct {
	Name  string `json:"name" validate:"required"`
	Count int    `json:"count" validate:"gte=1"`
}

func (JSONEchoRequest) ContentType() core.ContentType {
	return core.ApplicationJson
}

type QueryEchoRequest struct {
	Search string   `scheme:"search" validate:"required"`
	Limit  int      `scheme:"limit" validate:"gte=1,lte=100"`
	Tags   []string `scheme:"tags"`
}

func (QueryEchoRequest) ContentType() core.ContentType {
	return core.Query
}

type UploadRequest struct {
	Title string     `scheme:"title" validate:"required"`
	File  core.IFile `scheme:"file"`
}

func (UploadRequest) ContentType() core.ContentType {
	return core.MultipartFormData
}
