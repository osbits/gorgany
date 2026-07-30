package fixtureprovider

import (
	"context"
	"io"
	"net/http"

	"github.com/osbits/gorgany/app/core"
	grghttp "github.com/osbits/gorgany/http"

	// The engine this app uses, imported for its registration side effect. DbProvider no
	// longer registers any driver itself, so a Postgres-only app links only Postgres —
	// which is the point of the split, and the reason this is the single-engine package
	// rather than driver/builtin.
	_ "github.com/osbits/gorgany/db/sql/driver/postgres"
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

	// Deliberately only "Default" (F8).
	//
	// This app used to override ValidationErrors, ValidationError, InputBodyParseError and
	// InputParamParseError as well — copies of what the framework should have been doing —
	// and because http.GetErrorHandler prefers a registered handler, the e2e harness never
	// reached http/error.go's defaults at all. That is how processValidationErrors came to
	// be the one default handler C2 did not negotiate and nothing noticed: it answered
	// every caller, API clients included, with a 301 redirect to the Referer.
	//
	// The overrides are gone now that the defaults are equivalent, so this suite exercises
	// what an app with no error configuration actually gets. Customising "Default" is left
	// in place because that one is genuinely app-specific — and it keeps one registered
	// handler in the picture, so the custom-beats-default lookup stays covered too.
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

// defaultErrorHandler is the one handler this app overrides, and it is the example of how
// to write one: negotiate with grghttp.WantsJSON, and do not put an unclassified error's
// message in a production response.
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
