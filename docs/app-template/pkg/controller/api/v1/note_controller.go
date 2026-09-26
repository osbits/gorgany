package v1

import (
	"net/http"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/service/dto"

	"myapp/pkg/constant"
	command "myapp/pkg/model/command/json"
	"myapp/pkg/service"
)

type NoteController struct {
	Notes *service.NoteService `container:"inject"`
}

func NewNoteController() *NoteController { return &NoteController{} }

func (c *NoteController) GetRoutes() []core.IRouteConfig {
	signedIn := &middleware.AuthMiddleware{AuthStrategies: []string{core.DefaultKeyInRegistrar}}

	return []core.IRouteConfig{
		&router.RouteConfig{Namespace: constant.ApiNamespace, Path: constant.V1 + "/notes", Method: core.GET,
			Handler: c.Index, Name: "api.note.index"},
		&router.RouteConfig{Namespace: constant.ApiNamespace, Path: constant.V1 + "/notes/{id}", Method: core.GET,
			Handler: c.Show, Name: "api.note.show"},
		&router.RouteConfig{Namespace: constant.ApiNamespace, Path: constant.V1 + "/notes", Method: core.POST,
			Handler: c.Store, Name: "api.note.store", Middlewares: []core.IMiddleware{signedIn}},
	}
}

func (c *NoteController) Index(message core.HttpMessage) {
	notes, err := c.Notes.All()
	if err != nil {
		panic(err)
	}
	message.Response().JSON(dto.ReturnObject(notes, core.SuccessHttpStatus, nil), http.StatusOK)
}

// Show lets a *service.NotFoundError propagate: the recovery middleware hands it to
// the error handler registered for "NotFoundError", which answers 404.
func (c *NoteController) Show(message core.HttpMessage, id string) {
	note, err := c.Notes.Find(id)
	if err != nil {
		panic(err)
	}
	message.Response().JSON(dto.ReturnObject(note, core.SuccessHttpStatus, nil), http.StatusOK)
}

// Store takes its payload by value: the framework resolves DTO parameters with
// reflect.New(T), so a pointer parameter would never match core.HttpCommand.
func (c *NoteController) Store(message core.HttpMessage, body command.CreateNote) {
	note, err := c.Notes.Create(body.Title)
	if err != nil {
		panic(err)
	}
	message.Response().JSON(dto.ReturnObject(note, core.CreatedHttpStatus, nil), http.StatusCreated)
}
