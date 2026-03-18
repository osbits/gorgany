package gorgany

const FrameworkGit = "github.com/osbits/gorgany"

const FrameworkVersion = "1.3.3"

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
