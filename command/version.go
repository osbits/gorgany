package command

import (
	"fmt"
	"gorgany"
)

type VersionCommand struct {
}

func (thiz VersionCommand) GetName() string {
	return "version"
}

func (thiz VersionCommand) Execute() {
	fmt.Printf("Gorgany framework. Version %s\n", gorgany.FrameworkVersion)
}
