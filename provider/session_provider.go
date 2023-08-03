package provider

import (
	"gorgany/auth"
)

func NewSessionProvider() *SessionProvider {
	return &SessionProvider{}
}

type SessionProvider struct {
}

func (thiz SessionProvider) InitProvider() {
	//todo read type of storage from config and resolve it
	auth.SetAuthEntityService(FrameworkRegistrar.GetUserService())
	auth.SetSessionStorage(auth.NewMemorySession(), FrameworkRegistrar.GetSessionLifetime())
}
