package api

import (
	"encoding/json"

	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/service/dto"
	"github.com/osbits/gorgany/v2/util"
)

func NewLoginController() *LoginController {
	return &LoginController{}
}

type LoginController struct {
	authContext core.IAuthContext `container:"inject"`
	userService core.IUserService `container:"inject"`
}

// LoginPayload is the request body.
//
// The fields carry no json tags on purpose. This handler decodes with encoding/json, which
// matches a key to a field case-insensitively, so both {"username": ...} and
// {"Username": ...} have always worked. Routing it through the framework's own DTO parsing
// would be the better shape — it would inherit B3's negotiated 400 and validation for free —
// but that parser looks the key up exactly, so it would silently drop one of the two
// spellings and change this endpoint's wire contract. Left alone for that reason, not by
// oversight.
type LoginPayload struct {
	Username string
	Password string
}

// jwtStrategyName is the strategy this endpoint issues tokens through.
const jwtStrategyName = "jwt"

func (thiz LoginController) Login(message core.HttpMessage) {
	// The read error used to be discarded (`body, _ :=`), so an unreadable body produced an
	// empty payload and the handler carried on to look up the user "".
	body, err := message.Request().Body()
	if err != nil {
		thiz.badRequest(message, "The request body could not be read")
		return
	}

	loginPayload := &LoginPayload{}
	if err := json.Unmarshal(body, loginPayload); err != nil {
		// This used to be `panic(err)`. RecoveryMiddleware turns that into a 500, so
		// malformed JSON on the framework's own login route — the most probed route in any
		// deployed app — answered 500 where B3 made every other body path answer 400.
		//
		// The reason is reported, never the body: a login body that failed to parse is
		// precisely the one carrying a password. Same rule as
		// err.InputBodyParseError.Error(), which is used here to phrase it identically.
		thiz.badRequest(message, error2.NewInputBodyParseError(
			"", core.ApplicationJson.String(), err).Error())
		return
	}

	user, err := thiz.userService.GetByUsername(loginPayload.Username)
	if err != nil {
		thiz.unauthorized(message)
		return
	}

	if user == nil || !util.CompareSaltedHash(user.GetPassword(), loginPayload.Password) {
		thiz.unauthorized(message)
		return
	}

	strategy := thiz.authContext.Strategy(jwtStrategyName)
	if strategy == nil {
		// Reaching this means no "jwt" strategy is registered. Saying so beats
		// nil-dereferencing on the next line, which is the defect T3.3 fixed in
		// JwtMiddleware.
		thiz.internalError(message, "No `jwt` authentication strategy is registered")
		return
	}

	session, err := strategy.Login(user, message.Context())
	if err != nil {
		// err was assigned and never read. Login reports failure as (nil, err), so the
		// next line's session.GetId() dereferenced nil — a panic on the login path
		// whenever the strategy failed.
		thiz.internalError(message, "The session could not be created")
		return
	}
	if session == nil || session.GetId() == "" {
		// Previously answered with a Forbidden-coded envelope under HTTP 200, so a client
		// checking the status code saw success.
		message.Response().JSON(
			dto.ReturnObject(nil, core.ForbiddenHttpStatus, "Token has not been generated!"),
			core.ForbiddenHttpStatus.Status)
		return
	}

	message.Response().JSON(
		dto.ReturnObject(map[string]string{"access_token": session.GetId()},
			core.SuccessHttpStatus, nil),
		core.SuccessHttpStatus.Status)
}

// The failure responses below put the message in the envelope's `errors`, not its `body`. The
// previous `dto.ReturnObject("Unauthorized", core.NotAuthorizedHttpStatus, nil)` had it the
// other way round, which is the opposite of every other error response the framework emits.

func (thiz LoginController) unauthorized(message core.HttpMessage) {
	message.Response().JSON(
		dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, "Unauthorized"),
		core.NotAuthorizedHttpStatus.Status)
}

func (thiz LoginController) badRequest(message core.HttpMessage, reason string) {
	message.Response().JSON(
		dto.ReturnObject(nil, core.BadRequestHttpStatus, reason),
		core.BadRequestHttpStatus.Status)
}

func (thiz LoginController) internalError(message core.HttpMessage, reason string) {
	message.Response().JSON(
		dto.ReturnObject(nil, core.InternalErrorHttpStatus, reason),
		core.InternalErrorHttpStatus.Status)
}

func (thiz LoginController) GetRoutes() []core.IRouteConfig {
	corsMiddleware := middleware.NewCorsMiddleware(middleware.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
	})

	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:        "/{namespace:api}/v1/login",
			Method:      core.POST,
			Handler:     thiz.Login,
			Middlewares: []core.IMiddleware{corsMiddleware},
		},
	}
}
