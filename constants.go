package gorgany

// FrameworkGit is the framework's **module path**, despite the name — and it has to track
// the module path rather than the repository URL, because both its uses are module paths:
//
//   - command/db/diff.go builds the fully-qualified type name `<module>/model.File` to
//     compare against a GORM schema field's type, and reflect renders that from the import
//     path. Get it wrong and `db:diff` stops recognising model.File, so a File field is
//     treated as migratable and the generated DDL is wrong.
//   - the same file passes it into the migration template as FrameworkModuleName, so it is
//     emitted as an import in generated code.
//
// It therefore gained the /v2 suffix along with everything else.
const FrameworkGit = "github.com/osbits/gorgany/v2"

const FrameworkVersion = "2.1.0"

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
