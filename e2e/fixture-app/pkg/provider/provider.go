package fixtureprovider

import (
	"context"
	"io"
	"net/http"

	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	grghttp "github.com/osbits/gorgany/http"
	"github.com/osbits/gorgany/http/middleware"
	"github.com/osbits/gorgany/provider"
	"github.com/osbits/gorgany/service/dto"

	fixturemigration "github.com/osbits/gorgany/e2e/fixture-app/db/migration"
	fixtureseeder "github.com/osbits/gorgany/e2e/fixture-app/db/seeder"
	fixturehttp "github.com/osbits/gorgany/e2e/fixture-app/pkg/http"
	fixtureservice "github.com/osbits/gorgany/e2e/fixture-app/pkg/service"
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

// inputErrorHandler answers both InputBodyParseError and InputParamParseError with 400.
//
// It reports the *reason*, not err.Error(): InputBodyParseError.Error() includes the raw
// body for the log's benefit, and a body that failed to parse is exactly the kind that
// might carry a password halfway through. This is the pattern an app should copy.
func inputErrorHandler(err error, message core.HttpMessage) {
	reason := err.Error()
	if parseError, ok := err.(*error2.InputBodyParseError); ok {
		reason = "the request body could not be parsed"
		if parseError.RawError != nil {
			reason = parseError.RawError.Error()
		}
	}

	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, reason), http.StatusBadRequest)
		return
	}

	message.Response().Text(reason, http.StatusBadRequest)
}

func defaultErrorHandler(err error, message core.HttpMessage) {
	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), http.StatusInternalServerError)
		return
	}

	message.Response().Text("internal error", http.StatusInternalServerError)
}

// wantsJSON delegates to the framework's own negotiation rather than reimplementing it.
// The hand-rolled version here missed Accept and the api namespace, so a GET to an API
// route got an HTML-shaped error.
func wantsJSON(message core.HttpMessage) bool {
	return grghttp.WantsJSON(message)
}

type noopRenderer struct{}

func (r *noopRenderer) DoRender(_ context.Context, _ io.Writer, _ string, _ map[string]any) error {
	return nil
}

func (r *noopRenderer) RegisterGlobalFunction(_ string, _ any) {}

func (r *noopRenderer) RegisterGlobalVariable(_ string, _ any) {}
