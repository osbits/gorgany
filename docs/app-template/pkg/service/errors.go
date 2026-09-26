package service

// NotFoundError reports a missing entity. pkg/provider/error_provider.go maps it to
// a 404 by its type name.
//
// It lives here, not in pkg/domain: domains:register and db:diff treat every struct
// in pkg/domain as an entity to register and migrate.
type NotFoundError struct {
	Entity, ID string
}

func (e *NotFoundError) Error() string { return e.Entity + " " + e.ID + " not found" }
