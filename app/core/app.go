package core

import "context"

type IApplication interface {
	Run()
	Shutdown(ctx context.Context) error
}
