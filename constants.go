package gorgany

const FrameworkGit = "github.com/gorganyio/gorgany"

const FrameworkVersion = "1.1.8-alpha"

type ExecType string

const (
	Server ExecType = "server"
	Cli    ExecType = "cli"
)

type RunMode string

const (
	Dev  RunMode = "dev"
	Prod         = "prod"
)
