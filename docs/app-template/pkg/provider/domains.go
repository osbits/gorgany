package provider

import (
	"myapp/pkg/domain"
)

// Run `go run ./cmd/cli domains:register`
func generatedDomains() map[string]any {
	return map[string]any{
		"myapp/pkg/domain.Note": domain.Note{},
		"myapp/pkg/domain.User": domain.User{},
	}
}
