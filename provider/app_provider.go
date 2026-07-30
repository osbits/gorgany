package provider

import (
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/model"
	"github.com/osbits/gorgany/v2/service"
	"github.com/osbits/gorgany/v2/validator"
	"github.com/spf13/viper"
)

type AppProvider struct{}

func (a AppProvider) Register(container core.IContainer) {

	// Register session storage based on configuration
	sessionStorageType := viper.GetString("auth.session.storage")
	if sessionStorageType == "database" {
		// Register session repository for database storage
		err.HandleErrorWithStacktrace(container.SingletonLazy(func() auth.ISessionRepository {
			return auth.NewDbSessionRepository()
		}))

		// Register session mediator for automatic persistence
		err.HandleErrorWithStacktrace(container.SingletonLazy(func(repo auth.ISessionRepository) *auth.DbSessionMediator {
			return auth.NewDbSessionMediator(repo)
		}))

		err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.ISessionStorage {
			return auth.NewDbSessionStorage(time.Second * time.Duration(viper.GetInt("auth.session.lifeTime")))
		}))

		// Register session factory for database storage with mediator
		err.HandleErrorWithStacktrace(container.SingletonLazy(func(mediator *auth.DbSessionMediator) auth.ISessionFactory {
			factory := auth.NewDbSessionFactory()
			factory.SetMediator(mediator)
			return factory
		}))
	} else {
		// Default to memory storage
		err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.ISessionStorage {
			return auth.NewMemorySession(time.Second * time.Duration(viper.GetInt("auth.session.lifeTime")))
		}))

		// Register session factory for memory storage
		err.HandleErrorWithStacktrace(container.SingletonLazy(func() auth.ISessionFactory {
			return auth.NewMemorySessionFactory()
		}))
	}

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IAuthContext {
		return &auth.AuthContext{}
	}))

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IDomainContext {
		return &model.DomainContext{}
	}))

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IValidator {
		return validator.New()
	}))

	service.SetEmergencyContainerFactory(func() core.IEmergencyContainer {
		return container
	})

	err.HandleErrorWithStacktrace(container.SingletonLazy(func() core.IEmergencyContainer {
		return container
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
