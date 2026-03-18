package core

import "context"

// IApplication defines the interface for application lifecycle management
type IApplication interface {
	// Run starts the application
	Run()
	// Shutdown gracefully shuts down the application with the given context
	Shutdown(ctx context.Context) error
}
