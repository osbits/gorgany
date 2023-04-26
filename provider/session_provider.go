package provider

import (
	"graecoFramework/auth"
	"graecoFramework/auth/service"
)

func NewSessionProvider() *SessionProvider {
	return &SessionProvider{}
}

type SessionProvider struct {
}

func (thiz SessionProvider) InitProvider() {
	//todo read type of storage from config and resolve it
	service.SetAuthEntityService(FrameworkRegistrar.GetUserService())
	auth.SetSessionStorage(auth.NewMemorySession(), FrameworkRegistrar.GetSessionLifetime())
}
