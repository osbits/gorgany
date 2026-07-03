package gorgany

const FrameworkGit = "github.com/osbits/gorgany"

const FrameworkVersion = "1.5.0"

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
