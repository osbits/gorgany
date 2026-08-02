package provider

import (
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/log"
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
	// The JWT configuration is checked here, immediately before the strategy that depends on
	// it is armed, and the check panics rather than logging.
	//
	// This provider is where JwtAuthStrategy gets registered, so a boot that reaches the
	// registration with an unusable signing key is a boot that must not continue: every
	// alternative — a warning, a degraded mode, a strategy that fails per request — leaves an
	// operator who mistyped one environment variable running a server that accepts tokens
	// anyone can mint. Both execution modes come through here (ServerApp and ConsoleApp share
	// Bootstrapper.Bootstrap), so the CLI cannot be the one place a bad key is tolerated —
	// which matters because migrations and jobs run under the same configuration as the
	// server and, once past boot, the same code reads the same key.
	//
	// A panic is the framework's existing boot-failure mechanism; err.HandleError* only logs,
	// and a logged line at startup is exactly what nobody reads.
	// (Named jwtConfigErr because `err` is the error package in this file.)
	if jwtConfigErr := ValidateJwtConfig(); jwtConfigErr != nil {
		log.Log().Panicf("gorgany: refusing to boot: %v", jwtConfigErr)
	}

	container.Invoke(func(authContext core.IAuthContext) {
		stast := &auth.StandardAuthStrategy{}
		container.Make(stast)
		authContext.RegisterAuthStrategy(core.DefaultKeyInRegistrar, stast)

		jwtast := &auth.JwtAuthStrategy{}
		container.Make(jwtast)
		authContext.RegisterAuthStrategy("api", jwtast)
	})
}
