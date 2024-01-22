package gorgany

const FrameworkGit = "git.qix.sx/gorgany/gorgany.git"

const FrameworkVersion = "1.0.18"

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
