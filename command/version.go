package command

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git"
)

type VersionCommand struct {
}

func (thiz VersionCommand) GetName() string {
	return "version"
}

func (thiz VersionCommand) Execute() {
	fmt.Printf("Gorgany framework. Version %s\n", gorgany.FrameworkVersion)
}
