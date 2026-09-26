// Package command holds the application's console commands.
package command

import (
	"context"
	"fmt"

	"myapp/pkg/buildinfo"
)

// VersionCommand replaces the framework's built-in `version`, which prints the
// framework's version, with the application's own: the value the build stamped.
type VersionCommand struct{}

func (VersionCommand) GetName() string { return "version" }

func (VersionCommand) Execute(context.Context) { fmt.Println(buildinfo.Version) }
