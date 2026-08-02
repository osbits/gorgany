package http

import (
	"net/url"

	"github.com/osbits/gorgany/v2/app/core"
)

// PostFormValues returns the submitted form values, parsed at most once per request.
//
// Two parties want the form on a browser POST — a CSRF check that has to find the token, and
// the handler that has to read the fields — and net/http's ParseForm reads the body without
// putting it back, so whichever went first took it. That is not a hypothetical: mounting
// CSRFMiddleware in front of the login handler made every login fail, because the middleware
// parsed the form and the handler then read a body that was gone. Both go through here now.
//
// The optional-interface assertion is deliberate. Adding PostForm to core.IRequestScope would
// break every application that implements that interface, for a method almost none of them
// would want to write; the assertion costs one type check and leaves the contract alone. A
// scope that does not implement it gets the previous behaviour — read the body, parse it —
// which is correct as long as nothing else is competing for the body, and nothing else is,
// because the competitor was this framework's own middleware.
func PostFormValues(message core.HttpMessage) (url.Values, error) {
	if reader, ok := message.Request().(interface {
		PostForm() (url.Values, error)
	}); ok {
		return reader.PostForm()
	}

	body, err := message.Request().Body()
	if err != nil {
		return nil, err
	}
	return url.ParseQuery(string(body))
}
