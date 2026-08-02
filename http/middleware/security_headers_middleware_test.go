package middleware

import (
	"crypto/tls"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runSecurityHeaders(t *testing.T, options SecurityHeadersOptions, message *fakeMessage) *recordedResponse {
	t.Helper()

	reached := false
	handler := SecurityHeadersMiddleware{options: options}.Handle(func(core.HttpMessage) {
		reached = true
	})
	handler(message)

	require.True(t, reached, "the filter must call through")
	return message.recorded
}

// TestEveryResponseCarriesNosniff is the minimum the framework owed and emitted nowhere:
// a grep for X-Content-Type-Options over the whole tree returned nothing.
func TestEveryResponseCarriesNosniff(t *testing.T) {
	for _, target := range []string{"/", "/api/widgets", "/public/avatars/photo.png"} {
		recorded := runSecurityHeaders(t, SecurityHeadersOptions{}, newMessage("GET", target, nil))
		assert.Equal(t, "nosniff", recorded.Headers.Get("X-Content-Type-Options"), "on %s", target)
	}
}

func TestTheDefaultHeaderSetIsSafeAndNonBreaking(t *testing.T) {
	recorded := runSecurityHeaders(t, SecurityHeadersOptions{}, newMessage("GET", "/", nil))

	assert.Equal(t, "nosniff", recorded.Headers.Get("X-Content-Type-Options"))
	assert.Equal(t, "SAMEORIGIN", recorded.Headers.Get("X-Frame-Options"),
		"DENY would break a page that frames its own")
	assert.Equal(t, "strict-origin-when-cross-origin", recorded.Headers.Get("Referrer-Policy"))

	assert.Empty(t, recorded.Headers.Get("Content-Security-Policy"),
		"a default CSP cannot be both safe and non-breaking for a server-rendered app")
	assert.Empty(t, recorded.Headers.Get("Strict-Transport-Security"),
		"HSTS is meaningless on a plain-HTTP response")
}

// TestHstsIsSentOnlyOverTls, both ways round.
func TestHstsIsSentOnlyOverTls(t *testing.T) {
	t.Run("plain http", func(t *testing.T) {
		recorded := runSecurityHeaders(t, SecurityHeadersOptions{}, newMessage("GET", "/", nil))
		assert.Empty(t, recorded.Headers.Get("Strict-Transport-Security"))
	})

	t.Run("direct tls", func(t *testing.T) {
		message := newMessage("GET", "/", nil)
		message.req.raw.TLS = &tls.ConnectionState{}

		recorded := runSecurityHeaders(t, SecurityHeadersOptions{}, message)
		assert.Equal(t, "max-age=31536000", recorded.Headers.Get("Strict-Transport-Security"))
	})

	t.Run("behind a terminating proxy", func(t *testing.T) {
		recorded := runSecurityHeaders(t, SecurityHeadersOptions{},
			newMessage("GET", "/", map[string]string{"X-Forwarded-Proto": "https, http"}))
		assert.Equal(t, "max-age=31536000", recorded.Headers.Get("Strict-Transport-Security"))
	})

	t.Run("proxy reporting http", func(t *testing.T) {
		recorded := runSecurityHeaders(t, SecurityHeadersOptions{},
			newMessage("GET", "/", map[string]string{"X-Forwarded-Proto": "http"}))
		assert.Empty(t, recorded.Headers.Get("Strict-Transport-Security"))
	})

	t.Run("forwarded header not trusted", func(t *testing.T) {
		distrust := false
		recorded := runSecurityHeaders(t, SecurityHeadersOptions{TrustForwardedProto: &distrust},
			newMessage("GET", "/", map[string]string{"X-Forwarded-Proto": "https"}))
		assert.Empty(t, recorded.Headers.Get("Strict-Transport-Security"))
	})

	t.Run("suppressed", func(t *testing.T) {
		message := newMessage("GET", "/", nil)
		message.req.raw.TLS = &tls.ConnectionState{}

		recorded := runSecurityHeaders(t, SecurityHeadersOptions{HSTSMaxAge: -1}, message)
		assert.Empty(t, recorded.Headers.Get("Strict-Transport-Security"))
	})
}

func TestHstsDirectivesAreOptIn(t *testing.T) {
	message := newMessage("GET", "/", nil)
	message.req.raw.TLS = &tls.ConnectionState{}

	recorded := runSecurityHeaders(t, SecurityHeadersOptions{
		HSTSMaxAge:            600,
		HSTSIncludeSubdomains: true,
		HSTSPreload:           true,
	}, message)

	assert.Equal(t, "max-age=600; includeSubDomains; preload",
		recorded.Headers.Get("Strict-Transport-Security"))
}

func TestTheConfigurableHeadersAreConfigurable(t *testing.T) {
	recorded := runSecurityHeaders(t, SecurityHeadersOptions{
		ContentSecurityPolicy:           RecommendedContentSecurityPolicy,
		ContentSecurityPolicyReportOnly: "default-src 'self'; report-uri /csp",
		FrameOptions:                    "DENY",
		ReferrerPolicy:                  "no-referrer",
	}, newMessage("GET", "/", nil))

	assert.Equal(t, RecommendedContentSecurityPolicy, recorded.Headers.Get("Content-Security-Policy"))
	assert.Equal(t, "default-src 'self'; report-uri /csp",
		recorded.Headers.Get("Content-Security-Policy-Report-Only"))
	assert.Equal(t, "DENY", recorded.Headers.Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", recorded.Headers.Get("Referrer-Policy"))
}

// TestAHeaderCanBeSuppressed: "off" has to be distinguishable from the empty string a
// zero-valued options struct carries, or an app could never turn one of these off.
func TestAHeaderCanBeSuppressed(t *testing.T) {
	recorded := runSecurityHeaders(t, SecurityHeadersOptions{
		FrameOptions:   "off",
		ReferrerPolicy: "OFF",
	}, newMessage("GET", "/", nil))

	assert.Empty(t, recorded.Headers.Get("X-Frame-Options"))
	assert.Empty(t, recorded.Headers.Get("Referrer-Policy"))
	assert.Equal(t, "nosniff", recorded.Headers.Get("X-Content-Type-Options"),
		"nosniff is not one an app gets to switch off")
}

// TestHeadersAreWrittenBeforeTheHandler — a filter downstream that answers the request
// itself must still produce a response carrying them.
func TestHeadersAreWrittenBeforeTheHandler(t *testing.T) {
	message := newMessage("GET", "/", nil)

	var seen string
	handler := SecurityHeadersMiddleware{}.Handle(func(m core.HttpMessage) {
		seen = m.Response().Header().Get("X-Content-Type-Options")
		m.Response().Text("done", 403)
	})
	handler(message)

	assert.Equal(t, "nosniff", seen, "the headers must already be set when the handler runs")
	assert.Equal(t, 403, message.recorded.Status)
	assert.Equal(t, "nosniff", message.recorded.Headers.Get("X-Content-Type-Options"))
}
