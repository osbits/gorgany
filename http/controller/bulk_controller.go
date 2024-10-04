package controller

import (
	"encoding/json"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/model/command"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"github.com/spf13/viper"
	"io"
	"net/http"
	"strings"
	"sync"
)

type RequestClosure struct {
	Name    string
	Handler func() *model.ApiReturnObject
}

// BulkController - it used only for internal requests
type BulkController struct {
}

func (thiz BulkController) Parallel(message core.HttpMessage, cmd command.BulkCommand) {
	requestClosures := make([]RequestClosure, 0)

	for _, request := range cmd.Requests {
		url := router.GetRouter().UrlByName(request.Name, request.Params)
		if url == "" {
			message.ResponseJSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, fmt.Sprintf("Url [%s] not found", request.Name)), 200)
			return
		}
		requestClosures = append(requestClosures, RequestClosure{
			Name: request.Name,
			Handler: func() *model.ApiReturnObject {
				client := http.Client{}
				req, err := http.NewRequest(request.Method, fmt.Sprintf("%s%s", viper.GetString("app.server.host"), url), strings.NewReader(request.Body))
				if err != nil {
					return dto.ReturnObject(nil, core.InternalErrorHttpStatus, err)
				}

				req.Header = message.GetHeader()
				resp, err := client.Do(req)
				if err != nil {
					return dto.ReturnObject(nil, core.InternalErrorHttpStatus, err)
				}

				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return dto.ReturnObject(nil, core.InternalErrorHttpStatus, err)
				}
				var anyMap any
				json.Unmarshal(body, &anyMap)
				return dto.ReturnObject(anyMap, core.SuccessHttpStatus, nil)
			},
		})
	}

	wg := new(sync.WaitGroup)
	chRequests := make(chan RequestClosure)

	mu := sync.Mutex{}
	results := make(map[string]*model.ApiReturnObject, 0)
	workers := 5

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(waitGroup *sync.WaitGroup, channelRequest chan RequestClosure) {
			defer wg.Done()
			for request := range chRequests {
				ro := request.Handler()
				mu.Lock()
				results[request.Name] = ro
				mu.Unlock()
			}
		}(wg, chRequests)
	}

	for _, requestClosure := range requestClosures {
		chRequests <- requestClosure
	}

	close(chRequests)
	wg.Wait()

	message.ResponseJSON(dto.ReturnObject(results, core.SuccessHttpStatus, nil), 200)
}

func (thiz BulkController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:        "/bulk/parallel",
			Method:      core.POST,
			Handler:     thiz.Parallel,
			Middlewares: nil,
			Namespace:   string(core.Api),
			Name:        "api.bulk.parallel",
		},
	}
}
