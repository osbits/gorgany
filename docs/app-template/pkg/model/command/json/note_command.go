package json

import "github.com/osbits/gorgany/v2/app/core"

// CreateNote is the request body of POST /api/v1/notes.
type CreateNote struct {
	Title string `json:"title" validate:"required,min=3"`
}

func (CreateNote) ContentType() core.ContentType { return core.ApplicationJson }
