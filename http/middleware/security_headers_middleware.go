package middleware

import (
	"fmt"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
)

const (
	// DefaultReferrerPolicy sends the full URL to same-origin destinations and only the
	// origin across origins, which is what current browsers do anyway — stating it stops an
	// older one from leaking paths and query strings to third parties.
	DefaultReferrerPolicy = "strict-origin-when-cross-origin"

	// DefaultFrameOptions is SAMEORIGIN rather than DENY. DENY is the stronger answer to
	// clickjacking, and an app that frames none of its own pages should set it; it is not
	// the default because same-origin framing is ordinary (previews, embedded editors,
	// legacy admin screens) and a framework default that silently breaks a working page is
	// a default nobody keeps.
	DefaultFrameOptions = "SAMEORIGIN"

	// DefaultHSTSMaxAge is one year, the shortest value the preload list accepts.
	DefaultHSTSMaxAge = 31536000

	// RecommendedContentSecurityPolicy is a starting point for an app that is ready to
	// adopt CSP. It is *not* the default — see SecurityHeadersOptions.ContentSecurityPolicy.
	RecommendedContentSecurityPolicy = "default-src 'self'; object-src 'none'; " +
		"base-uri 'self'; frame-ancestors 'self'; form-action 'self'"
)

// SecurityHeadersOptions configures SecurityHeadersMiddleware. The zero value is the set of
// defaults described on each field, so an app that wants them writes
// NewSecurityHeadersMiddleware(SecurityHeadersOptions{}).
type SecurityHeadersOptions struct {
	// ContentSecurityPolicy is sent as Content-Security-Policy. Empty means no CSP header.
	//
	// It is off by default, and that is a deliberate asymmetry with the other headers here.
	// A CSP worth having forbids inline script and inline style, and a server-rendered
	// application built with this framework's own view engines routinely has both — so any
	// default policy strict enough to be worth the bytes would break working pages on
	// upgrade, and one loose enough not to (`unsafe-inline`, `unsafe-eval`) buys close to
	// nothing. A policy has to be written against a particular app's pages; there is no
	// safe-and-invisible value to pick on its behalf. RecommendedContentSecurityPolicy is a
	// reasonable place to start, and ContentSecurityPolicyReportOnly is how to find out what
	// it would break before it breaks it.
	ContentSecurityPolicy string

	// ContentSecurityPolicyReportOnly is sent as Content-Security-Policy-Report-Only. A
	// policy set here is evaluated and reported by the browser but never enforced, which is
	// how to measure a candidate policy against real traffic.
	ContentSecurityPolicyReportOnly string

	// FrameOptions is sent as X-Frame-Options. Empty means DefaultFrameOptions; "off"
	// suppresses the header.
	FrameOptions string

	// ReferrerPolicy is sent as Referrer-Policy. Empty means DefaultReferrerPolicy; "off"
	// suppresses the header.
	ReferrerPolicy string

	// HSTSMaxAge is the max-age of Strict-Transport-Security in seconds. Zero means
	// DefaultHSTSMaxAge; a negative value suppresses the header.
	//
	// The header is only ever sent over TLS. A browser ignores it on a plain-HTTP response
	// per the spec, but emitting it there would also mean a reverse proxy that terminates
	// TLS and forwards cleartext could not tell the framework apart from one that had
	// decided the site was HTTPS — so the decision is made from the request, and
	// X-Forwarded-Proto is honoured for exactly that deployment.
	HSTSMaxAge int

	// HSTSIncludeSubdomains adds includeSubDomains. Off by default: the directive commits
	// every subdomain of this host to HTTPS for max-age, including ones this app knows
	// nothing about.
	HSTSIncludeSubdomains bool

	// HSTSPreload adds preload. Off by default, because submitting a host to the preload
	// list is close to irreversible and is not a decision a default should make.
	HSTSPreload bool

	// TrustForwardedProto makes the TLS decision for HSTS trust X-Forwarded-Proto. On by
	// default, since terminating TLS at a proxy is the normal deployment; turn it off for a
	// server facing the internet directly, where a client could set the header itself.
	//
	// Getting this wrong is not dangerous in the direction that matters: a spoofed
	// X-Forwarded-Proto makes the framework send HSTS on a response the browser then
	// ignores, because the browser applies the header only to responses it received over
	// TLS itself.
	TrustForwardedProto *bool
}

// SecurityHeadersMiddleware sets the response headers that keep a browser from treating this
// app's output more permissively than it should.
//
// The framework emitted none of them: no X-Content-Type-Options, no Content-Security-Policy,
// no X-Frame-Options, no Strict-Transport-Security, no Referrer-Policy anywhere in the tree.
// nosniff is the one whose absence was actively exploitable — it is what stops a browser
// from ignoring a deliberately inert Content-Type and rendering uploaded bytes as HTML — but
// with none of the others there was no second line of defence behind any of it either.
//
// The headers are written before the handler runs, so they are present on early returns from
// middleware further down the chain, and so a handler that wants a different value can still
// overwrite one.
//
// RouteProvider registers this as a /** filter automatically; call
// RouteProvider.DisableSecurityHeadersMiddleware() to opt out and configure your own.
type SecurityHeadersMiddleware struct {
	options SecurityHeadersOptions
}

// NewSecurityHeadersMiddleware creates the middleware.
func NewSecurityHeadersMiddleware(options SecurityHeadersOptions) *SecurityHeadersMiddleware {
	return &SecurityHeadersMiddleware{options: options}
}

var _ core.IMiddleware = (*SecurityHeadersMiddleware)(nil)

// Handle implements the middleware handler.
func (thiz SecurityHeadersMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		thiz.apply(message)
		next(message)
	}
}

func (thiz SecurityHeadersMiddleware) apply(message core.HttpMessage) {
	if message == nil {
		return
	}

	response := message.Response()
	if response == nil {
		return
	}

	// Not configurable, and not optional. Every other header here is a policy an app might
	// legitimately want to shape; this one only ever says "the Content-Type I sent is the
	// Content-Type I meant".
	response.SetHeader("X-Content-Type-Options", "nosniff")

	if value := headerValue(thiz.options.FrameOptions, DefaultFrameOptions); value != "" {
		response.SetHeader("X-Frame-Options", value)
	}

	if value := headerValue(thiz.options.ReferrerPolicy, DefaultReferrerPolicy); value != "" {
		response.SetHeader("Referrer-Policy", value)
	}

	if thiz.options.ContentSecurityPolicy != "" {
		response.SetHeader("Content-Security-Policy", thiz.options.ContentSecurityPolicy)
	}
	if thiz.options.ContentSecurityPolicyReportOnly != "" {
		response.SetHeader("Content-Security-Policy-Report-Only", thiz.options.ContentSecurityPolicyReportOnly)
	}

	if value := thiz.strictTransportSecurity(message); value != "" {
		response.SetHeader("Strict-Transport-Security", value)
	}
}

func (thiz SecurityHeadersMiddleware) strictTransportSecurity(message core.HttpMessage) string {
	if thiz.options.HSTSMaxAge < 0 {
		return ""
	}
	if !thiz.isSecure(message) {
		return ""
	}

	maxAge := thiz.options.HSTSMaxAge
	if maxAge == 0 {
		maxAge = DefaultHSTSMaxAge
	}

	value := fmt.Sprintf("max-age=%d", maxAge)
	if thiz.options.HSTSIncludeSubdomains {
		value += "; includeSubDomains"
	}
	if thiz.options.HSTSPreload {
		value += "; preload"
	}
	return value
}

func (thiz SecurityHeadersMiddleware) isSecure(message core.HttpMessage) bool {
	request := message.Request()
	if request == nil {
		return false
	}

	if raw := request.RawRequest(); raw != nil && raw.TLS != nil {
		return true
	}

	trustForwarded := true
	if thiz.options.TrustForwardedProto != nil {
		trustForwarded = *thiz.options.TrustForwardedProto
	}
	if !trustForwarded {
		return false
	}

	headers := request.Header()
	if headers == nil {
		return false
	}

	// The first entry is the one the outermost proxy wrote for the client-facing hop.
	forwarded := headers.Get("X-Forwarded-Proto")
	if forwarded == "" {
		return false
	}
	first := forwarded
	if comma := strings.IndexByte(forwarded, ','); comma >= 0 {
		first = forwarded[:comma]
	}
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

// headerValue resolves a configured value against its default, with "off" as the way to
// suppress a header entirely — distinguishable from the empty string, which means "use the
// default" and is what a zero-valued options struct carries.
func headerValue(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	if strings.EqualFold(strings.TrimSpace(configured), "off") {
		return ""
	}
	return configured
}
