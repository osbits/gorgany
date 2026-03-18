package command

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git"
)

type VersionCommand struct {
}

func (thiz VersionCommand) GetName() string {
	return "version"
}

func (thiz VersionCommand) Execute(ctx context.Context) {
	fmt.Printf("Gorgany framework. Version %s\n", gorgany.FrameworkVersion)
}
