package provider

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/controller"
	"github.com/osbits/gorgany/v2/http/middleware"
	"gopkg.in/yaml.v3"

	"myapp/test/testkit"
)

// publicAPI lists the API operations that deliberately need no sign-in. Adding a
// line here is a security decision; review it as one.
var publicAPI = []string{
	"GET /api/v1/notes",
	"GET /api/v1/notes/{id}",
}

// TestRouteInventory keeps api/routes.txt equal to the routes the app registers,
// so every route change shows up in review. It also rejects duplicate route names:
// the router keeps only the last route registered under a name.
func TestRouteInventory(t *testing.T) {
	names := map[string]string{}
	var lines []string
	for _, route := range routes() {
		op := operation(route)
		if prev, dup := names[route.GetName()]; dup {
			t.Errorf("route name %q is used by %q and %q", route.GetName(), prev, op)
		}
		names[route.GetName()] = op
		lines = append(lines, strings.TrimRight(fmt.Sprintf("%-32s %-20s %s", op, route.GetName(), middlewareNames(route)), " "))
	}
	sort.Strings(lines)
	testkit.Golden(t, "../../api/routes.txt", strings.Join(lines, "\n")+"\n")
}

// TestHandlersAreResolvable runs the check the router runs at boot, so a handler
// whose parameters the framework cannot bind fails `go test` instead of the deploy.
func TestHandlersAreResolvable(t *testing.T) {
	for _, route := range routes() {
		if err := grghttp.ValidateHandlerParameters(route.GetHandler()); err != nil {
			t.Errorf("%s: %v", operation(route), err)
		}
	}
}

// TestAPIRoutesRequireSignIn fails when an API route has no AuthMiddleware and is
// not listed in publicAPI, and when publicAPI lists a route nobody serves: a stale
// line would silently exempt the next route that happens to take its place.
func TestAPIRoutesRequireSignIn(t *testing.T) {
	served := map[string]bool{}
	for _, route := range routes() {
		if route.GetNamespace() != "api" {
			continue
		}
		served[operation(route)] = true
		if !slices.Contains(publicAPI, operation(route)) && !slices.ContainsFunc(route.GetMiddlewares(), isAuth) {
			t.Errorf("%s has no AuthMiddleware; add one or list it in publicAPI", operation(route))
		}
	}
	for _, op := range publicAPI {
		if !served[op] {
			t.Errorf("publicAPI lists %q, which is not served: delete the line", op)
		}
	}
}

// TestAPIRoutesMatchTheSpec fails when api/openapi.yaml and the registered API
// routes disagree in either direction.
func TestAPIRoutesMatchTheSpec(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	documented := map[string]bool{}
	for path, item := range spec.Paths {
		for method := range item {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	registered := map[string]bool{}
	for _, route := range routes() {
		if route.GetNamespace() == "api" {
			registered[operation(route)] = true
		}
	}

	for op := range registered {
		if !documented[op] {
			t.Errorf("%s is served but not in api/openapi.yaml", op)
		}
	}
	for op := range documented {
		if !registered[op] {
			t.Errorf("%s is in api/openapi.yaml but not served", op)
		}
	}
}

// routes is every route the server answers: the app's controllers, plus GET /csrf,
// which RouteProvider mounts itself unless DisableCsrfController is called.
func routes() []core.IRouteConfig {
	var all []core.IRouteConfig
	for _, c := range append(controllers(), controller.NewCsrfController()) {
		all = append(all, c.GetRoutes()...)
	}
	return all
}

var paramPattern = regexp.MustCompile(`\{(\w+):[^}]+\}`)

// operation renders a route as "METHOD /path" the way a client sees it: the
// namespace becomes a path segment and {id:[0-9]+} becomes {id}.
func operation(route core.IRouteConfig) string {
	path := route.GetPath()
	if ns := route.GetNamespace(); ns != "" {
		path = "/" + ns + path
	}
	return fmt.Sprintf("%s %s", route.GetMethod(), paramPattern.ReplaceAllString(path, "{$1}"))
}

func isAuth(m core.IMiddleware) bool {
	switch m.(type) {
	case *middleware.AuthMiddleware, middleware.AuthMiddleware:
		return true
	}
	return false
}

func middlewareNames(route core.IRouteConfig) string {
	var names []string
	for _, m := range route.GetMiddlewares() {
		names = append(names, reflect.TypeOf(m).String())
	}
	return strings.Join(names, ",")
}
