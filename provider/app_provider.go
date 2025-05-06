package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/auth"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/validator"
	"github.com/spf13/viper"
)

type AppProvider struct{}

func (a AppProvider) Register(container core.IContainer) {

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.ISessionStorage {
		return auth.NewMemorySession(viper.GetDuration("auth.session.lifeTime"))
	}))

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IAuthContext {
		return &auth.AuthContext{}
	}))

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IDomainContext {
		return &model.DomainContext{}
	}))

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IValidator {
		return validator.New()
	}))
}

func (a AppProvider) Boot(container core.IContainer) {
	container.Invoke(func(authContext core.IAuthContext) {
		stast := &auth.StandardAuthStrategy{}
		container.Make(stast)
		authContext.RegisterAuthStrategy(core.DefaultKeyInRegistrar, stast)

		jwtast := &auth.JwtAuthStrategy{}
		container.Make(jwtast)
		authContext.RegisterAuthStrategy("api", jwtast)
	})
}
