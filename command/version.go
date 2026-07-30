package command

import (
	"context"
	"fmt"
	"github.com/osbits/gorgany/v2"
)

type VersionCommand struct {
}

func (thiz VersionCommand) GetName() string {
	return "version"
}

func (thiz VersionCommand) Execute(ctx context.Context) {
	fmt.Printf("Gorgany framework. Version %s\n", gorgany.FrameworkVersion)
}
