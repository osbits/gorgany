package command

import "fmt"

type VersionCommand struct {
}

func (thiz VersionCommand) GetName() string {
	return "version"
}

func (thiz VersionCommand) Execute() {
	fmt.Println("Gorgany framework. Version 1.0")
}
