package provider

import "graecoFramework/auth"

func NewSessionProvider() *SessionProvider {
	return &SessionProvider{}
}

type SessionProvider struct {
}

func (thiz SessionProvider) InitProvider() {
	//todo read type of storage from config and resolve it
	auth.SetSessionStorage(auth.NewMemorySession(), FrameworkRegistrar.GetSessionLifetime())
}
