package middleware

import (
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/service/dto"
	"github.com/spf13/viper"
)

// JwtMiddleware authenticates a request from a Bearer token and, when Roles is
// set, authorises it.
//
// The JwtService is injected by the container. It used to be constructed inline
// with auth.NewJwtService(), which produced a service whose own
// `userService core.IUserService` field was never filled — so the role check
// dereferenced nil and the documented way to use this middleware was the way that
// crashed.
type JwtMiddleware struct {
	Roles []core.UserRole

	// JwtService is resolved from the container. Leave it nil and the container
	// fills it; set it explicitly in tests.
	JwtService *auth.JwtService `container:"inject"`
}

var _ core.IMiddleware = (*JwtMiddleware)(nil)

const bearerPrefix = "Bearer "

func (thiz JwtMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		jwtService := thiz.JwtService
		if jwtService == nil {
			// Reaching this means the middleware was constructed outside the
			// container. Say so through the error chain rather than
			// nil-dereferencing a few lines later.
			panic(error2.NewJwtAuthError())
		}

		token := strings.TrimSpace(message.Request().Header().Get("Authorization"))
		if len(token) > len(bearerPrefix) && strings.EqualFold(token[:len(bearerPrefix)], bearerPrefix) {
			token = strings.TrimSpace(token[len(bearerPrefix):])
		}

		if token == "" {
			panic(error2.NewJwtAuthError())
		}

		secret := viper.GetString("auth.jwt.secret")
		if !jwtService.ValidateJwt(token, secret) {
			panic(error2.NewJwtAuthError())
		}

		if len(thiz.Roles) == 0 {
			next(message)
			return
		}

		// The role check goes through the user service, not the token's `role` claim,
		// even though GenerateJwt now writes one (C6). A signed token is immutable for
		// its whole lifetime, so trusting the claim would let a demoted admin stay an
		// admin for up to auth.jwt.lifeTime. The lookup is what makes a role change take
		// effect immediately; see auth.RoleFromClaims for the claim's intended use.
		user, err := jwtService.GetUser(token, secret)
		if err != nil {
			panic(error2.NewJwtAuthError())
		}
		// IUserService.Get/GetByUsername are documented as never returning
		// (nil, nil), but an app supplies that implementation, so a nil user is
		// treated as "not authenticated" rather than dereferenced.
		if user == nil {
			panic(error2.NewJwtAuthError())
		}

		for _, role := range thiz.Roles {
			if role == user.GetRole() {
				next(message)
				return
			}
		}

		message.Response().JSON(
			dto.ReturnObject(nil, core.ForbiddenHttpStatus, nil),
			core.ForbiddenHttpStatus.Status,
		)
	}
}
