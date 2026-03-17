package fixtureprovider

import (
	"context"
	"io"
	"net/http"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	grghttp "git.qix.sx/gorgany/gorgany.git/http"
	"git.qix.sx/gorgany/gorgany.git/http/middleware"
	"git.qix.sx/gorgany/gorgany.git/provider"
	"git.qix.sx/gorgany/gorgany.git/service/dto"

	fixturemigration "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/db/migration"
	fixtureseeder "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/db/seeder"
	fixturehttp "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/pkg/http"
	fixtureservice "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/pkg/service"
)

func NewBootstrapper() *provider.Bootstrapper {
	bootstrapper := provider.NewGorganyBootstrapper()

	routeProvider := provider.NewRouteProvider()
	routeProvider.SetNotFoundHandler(fixturehttp.NotFound)
	routeProvider.AddMiddleware(
		grghttp.NewMiddlewareConfigBuilder().
			WithPattern("/**").
			AsFilter().
			WithMiddleware(middleware.NewSessionMiddleware()).
			Build(),
	)
	routeProvider.AddController(fixturehttp.NewWebController())
	routeProvider.AddController(fixturehttp.NewAPIController())

	errorProvider := provider.NewErrorProvider()
	errorProvider.AddHandler("ValidationErrors", validationErrorsHandler)
	errorProvider.AddHandler("ValidationError", validationErrorHandler)
	errorProvider.AddHandler("InputBodyParseError", inputErrorHandler)
	errorProvider.AddHandler("InputParamParseError", inputErrorHandler)
	errorProvider.AddHandler("Default", defaultErrorHandler)

	bootstrapper.AddProvider(provider.NewLoggerProvider())
	bootstrapper.AddProvider(errorProvider)
	bootstrapper.AddProvider(provider.AppProvider{})
	bootstrapper.AddProvider(provider.NewDbProvider())
	bootstrapper.AddProvider(provider.NewCommandProvider())
	bootstrapper.AddProvider(routeProvider)
	bootstrapper.AddProvider(&FixtureProvider{})

	return bootstrapper
}

type FixtureProvider struct{}

func (p *FixtureProvider) Register(container core.IContainer) {
	_ = container.SingletonLazy(func() core.IEngineRenderer {
		return &noopRenderer{}
	})
	_ = container.SingletonLazy(func() *fixtureservice.WidgetService {
		return &fixtureservice.WidgetService{}
	})
	_ = container.SingletonLazy(func() core.IUserService {
		return &fixtureservice.UserService{}
	})
}

func (p *FixtureProvider) Boot(container core.IContainer) {
	_ = container.Invoke(func(dataContext core.IDataContext, webContext core.IWebContext) {
		dataContext.AddMigration(fixturemigration.NewFixtureMigration())
		dataContext.AddSeeder(fixtureseeder.NewFixtureSeeder())
		webContext.SetHomeUrl("/web/protected")
	})
}

func validationErrorsHandler(err error, message core.HttpMessage) {
	validationErrors, ok := err.(*error2.ValidationErrors)
	if !ok {
		dto := dto.ReturnObject(nil, core.ValidationHttpStatus, err.Error())
		message.Response().JSON(dto, http.StatusUnprocessableEntity)
		return
	}

	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, core.ValidationHttpStatus, *validationErrors), http.StatusUnprocessableEntity)
		return
	}

	message.Response().Text(validationErrors.Error(), http.StatusUnprocessableEntity)
}

func validationErrorHandler(err error, message core.HttpMessage) {
	if validationErr, ok := err.(*error2.ValidationError); ok {
		if wantsJSON(message) {
			message.Response().JSON(dto.ReturnObject(nil, core.ValidationHttpStatus, []error2.ValidationError{*validationErr}), http.StatusUnprocessableEntity)
			return
		}
		message.Response().Text(validationErr.Error(), http.StatusUnprocessableEntity)
		return
	}

	validationErrorsHandler(err, message)
}

func inputErrorHandler(err error, message core.HttpMessage) {
	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, err.Error()), http.StatusBadRequest)
		return
	}

	message.Response().Text(err.Error(), http.StatusBadRequest)
}

func defaultErrorHandler(err error, message core.HttpMessage) {
	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}

	message.Response().Text("internal error", http.StatusInternalServerError)
}

func wantsJSON(message core.HttpMessage) bool {
	req := message.Request().RawRequest()
	return strings.HasPrefix(req.URL.Path, "/api/") || strings.Contains(req.Header.Get("Content-Type"), core.ApplicationJson.String())
}

type noopRenderer struct{}

func (r *noopRenderer) DoRender(_ context.Context, _ io.Writer, _ string, _ map[string]any) error {
	return nil
}

func (r *noopRenderer) RegisterGlobalFunction(_ string, _ any) {}

func (r *noopRenderer) RegisterGlobalVariable(_ string, _ any) {}
