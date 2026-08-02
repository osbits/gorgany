package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/spf13/viper"
)

// SameOriginMiddleware refuses a state-changing request that did not come from this origin.
//
// It exists because the built-in browser login had no CSRF protection at all and could not
// easily be given the token kind. An attacker submits *their own* valid credentials from a
// page the victim visits; the victim's browser sends the POST, the login succeeds, and the
// victim spends the visit inside the attacker's account — typing into it, uploading to it,
// with the attacker holding the other half of the session. Nothing about the traffic looks
// unusual.
//
// Why not simply mount CSRFMiddleware there:
//
//  1. The token cannot exist yet. The framework ships no login template — applications own
//     theirs — and until this release there was no view helper that could emit the hidden
//     field, so the field the middleware looks for would be absent from every existing app's
//     form and every login would 403.
//  2. CSRFMiddleware requires a session, correctly, and refuses when there is none. But
//     SessionMiddleware is not in the default middleware set, so on a default wiring a login
//     POST legitimately arrives without one — a first-time visitor would be refused by
//     construction. The audit's own requirement, that login protection be independent of
//     whether the request already has a session, is unsatisfiable with a session-bound token
//     and satisfied trivially by an origin check.
//
// An origin check needs nothing of the template, nothing of the client and no session. It is
// weaker than a token against an attacker who can already run script on a same-origin page —
// but such an attacker has the token too, so the token buys nothing there either.
type SameOriginMiddleware struct {
	SameOriginOptions
}

// SameOriginOptions configures the check.
type SameOriginOptions struct {
	// AllowSameSite admits a request from a sibling host under the same registrable domain.
	//
	// Off by default, and that is the point of the middleware over the cookie attribute
	// alone: the session cookie is SameSite=Lax, which already stops a cross-*site* form POST,
	// so a check that also allowed same-site would add nothing. What it adds is the sibling
	// subdomain — the gap SameSite cannot see and the one the framework's own session-fixation
	// notes describe as the realistic foothold.
	AllowSameSite bool

	// RequireOriginHeader refuses a request carrying none of Sec-Fetch-Site, Origin or
	// Referer, instead of allowing it.
	//
	// Off by default. A browser cannot be made to omit all three on a cross-site form POST, so
	// the all-absent case is a non-browser client — curl, a smoke test, a native app — and
	// refusing those by default would break them for no gain against the attack. Turn it on
	// when the endpoint is browser-only. Residual, stated plainly: a pre-2020 browser that
	// sends no Origin on POST, driven from a page carrying `<meta name="referrer"
	// content="no-referrer">`, slips through with this off.
	RequireOriginHeader bool

	// TrustForwardedProto reads the scheme from X-Forwarded-Proto.
	//
	// Off by default, and deliberately not inferred. The header is client-settable unless a
	// proxy overwrites it, so trusting it unconditionally lets a caller assert `https` and
	// match a configured https origin from a plaintext request. Turn it on only when a proxy
	// in front of the app sets it.
	TrustForwardedProto bool

	// ServerURL is this app's own origin. Empty means derive it from the request's Host.
	ServerURL string
}

// NewSameOriginMiddleware builds the check with configuration-supplied defaults.
func NewSameOriginMiddleware() *SameOriginMiddleware {
	return &SameOriginMiddleware{SameOriginOptions{
		AllowSameSite:       viper.GetBool("http.security.origin.allowSameSite"),
		RequireOriginHeader: viper.GetBool("http.security.origin.required"),
		TrustForwardedProto: viper.GetBool("http.security.origin.trustForwardedProto"),
		ServerURL:           viper.GetString("app.server.url"),
	}}
}

// NewSameOriginMiddlewareWith builds the check with explicit options.
func NewSameOriginMiddlewareWith(options SameOriginOptions) *SameOriginMiddleware {
	return &SameOriginMiddleware{options}
}

// Handle implements the middleware.
func (thiz SameOriginMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		request := message.Request().RawRequest()
		if request == nil {
			next(message)
			return
		}

		if isSafeMethod(request.Method) {
			next(message)
			return
		}

		if reason := thiz.refuse(request); reason != "" {
			grghttp.WriteNegotiatedError(message, core.ForbiddenHttpStatus, reason)
			return
		}

		next(message)
	}
}

// isSafeMethod reports a method that does not change state, and so cannot be forged into
// doing so.
func isSafeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// refuse returns the reason to refuse, or "" to allow.
//
// The three signals are tried in order of how much they can be trusted. Sec-Fetch-Site is a
// forbidden header name — script cannot set it — and says exactly what is being asked; Origin
// is next; Referer last, because it is the one a page can suppress.
func (thiz SameOriginMiddleware) refuse(request *http.Request) string {
	switch request.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return ""
	case "none":
		// A typed URL, a bookmark, a browser-initiated navigation. Not something another
		// site can cause.
		return ""
	case "cross-site":
		return "This request came from another site."
	case "same-site":
		if thiz.AllowSameSite {
			return ""
		}
		return "This request came from another host on this domain."
	}

	own := thiz.ownOrigin(request)

	if origin := request.Header.Get("Origin"); origin != "" {
		// "null" is what a sandboxed iframe, a data: document or a redirected cross-origin
		// request sends. It is not this origin.
		if origin == "null" || !sameOrigin(origin, own) {
			return "This request came from another origin."
		}
		return ""
	}

	if referer := request.Header.Get("Referer"); referer != "" {
		if !sameOrigin(referer, own) {
			return "This request came from another origin."
		}
		return ""
	}

	if thiz.RequireOriginHeader {
		return "This request did not say where it came from."
	}
	return ""
}

// ownOrigin is the origin this request was addressed to.
func (thiz SameOriginMiddleware) ownOrigin(request *http.Request) string {
	if thiz.ServerURL != "" {
		return normaliseOrigin(thiz.ServerURL)
	}

	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if thiz.TrustForwardedProto {
		if forwarded := request.Header.Get("X-Forwarded-Proto"); forwarded != "" {
			scheme = strings.ToLower(strings.TrimSpace(strings.Split(forwarded, ",")[0]))
		}
	}

	return scheme + "://" + strings.ToLower(request.Host)
}

// sameOrigin compares a URL against an origin by scheme, host and port.
func sameOrigin(candidate, own string) bool {
	if own == "" {
		return false
	}
	return normaliseOrigin(candidate) == normaliseOrigin(own)
}

// normaliseOrigin reduces a URL to scheme://host[:port], lower-cased, with the default port
// for the scheme removed so http://x and http://x:80 compare equal.
func normaliseOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()

	switch {
	case port == "":
	case scheme == "http" && port == "80":
		port = ""
	case scheme == "https" && port == "443":
		port = ""
	}

	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}
