package db

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"gorm.io/gorm"
)

// DatasourceFlag is the flag name every db command accepts to choose which
// configured connection it operates on.
const DatasourceFlag = "datasource"

// StepsFlag is the flag `db:migrate down` accepts to choose how many migrations
// to roll back.
const StepsFlag = "steps"

// DatasourceScoped is the optional interface a migration or seeder implements to
// declare which datasource it targets.
//
// A migration written for a second database used to execute against the first,
// because migrate/seed/diff hard-coded core.DefaultKeyInRegistrar — a Postgres DDL
// statement fired at a MySQL connection with no warning. A migration that declares
// its target is skipped when a different datasource is selected, and refused
// outright when the datasource it names is not configured at all.
//
// A migration that declares nothing is treated as targeting `default`, which keeps
// every existing migration behaving exactly as before and means
// `db:migrate up --datasource=other` cannot sweep them onto the wrong engine.
type DatasourceScoped interface {
	// DataSourceName returns the name of the datasource this migration or seeder
	// targets, as written under the `databases` config key.
	DataSourceName() string
}

// TargetDatasourceOf returns the datasource a migration or seeder targets,
// defaulting to core.DefaultKeyInRegistrar when it declares nothing.
func TargetDatasourceOf(v any) string {
	if scoped, ok := v.(DatasourceScoped); ok {
		if name := strings.TrimSpace(scoped.DataSourceName()); name != "" {
			return name
		}
	}
	return core.DefaultKeyInRegistrar
}

// SelectedDatasource returns the datasource name the command line selected,
// defaulting to core.DefaultKeyInRegistrar.
//
// It scans os.Args directly rather than going through command.Resolver's flag
// mechanism, because that mechanism hands os.Args[2:] to flag.FlagSet.Parse and
// Go's flag package stops at the first non-flag argument — which for
// `db:migrate up --datasource=x` is the positional `up`, so the flag would never
// be seen. Both `--datasource=x` and `-datasource x` spellings are accepted.
func SelectedDatasource() string {
	if name, ok := lookupFlag(os.Args, DatasourceFlag); ok && name != "" {
		return name
	}
	return core.DefaultKeyInRegistrar
}

// SelectedSteps returns the number of migrations `db:migrate down` should roll
// back, defaulting to 1.
func SelectedSteps() (int, error) {
	raw, ok := lookupFlag(os.Args, StepsFlag)
	if !ok || raw == "" {
		return 1, nil
	}

	steps, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("--%s must be an integer, got %q", StepsFlag, raw)
	}
	if steps < 1 {
		return 0, fmt.Errorf("--%s must be at least 1, got %d", StepsFlag, steps)
	}
	return steps, nil
}

// lookupFlag finds `--name=value`, `-name=value`, `--name value` or `-name value`
// anywhere in args.
func lookupFlag(args []string, name string) (string, bool) {
	for i, arg := range args {
		for _, prefix := range []string{"--" + name, "-" + name} {
			if arg == prefix {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					return args[i+1], true
				}
				return "", true
			}
			if strings.HasPrefix(arg, prefix+"=") {
				return strings.TrimPrefix(arg, prefix+"="), true
			}
		}
	}
	return "", false
}

// ResolveGorm returns the *gorm.DB behind the named datasource.
//
// An unknown name is an error naming the flag, rather than a nil dereference: the
// commands used to call GetDataSource(core.DefaultKeyInRegistrar) unconditionally
// and would panic on a nil datasource if `default` was not configured.
func ResolveGorm(dbContext core.IDBContext, name string) (*gorm.DB, error) {
	if dbContext == nil {
		return nil, fmt.Errorf("no database context available")
	}

	dataSource := dbContext.GetDataSource(name)
	if dataSource == nil {
		return nil, fmt.Errorf(
			"datasource %q is not configured; check the `databases` config key and the --%s flag",
			name, DatasourceFlag)
	}

	driver, err := dataSource.GetDriver()
	if err != nil {
		return nil, fmt.Errorf("datasource %q: %w", name, err)
	}

	gormInstance, ok := driver.(*gorm.DB)
	if !ok {
		return nil, fmt.Errorf(
			"datasource %q is not GORM-backed (driver is %T), so this command cannot run against it",
			name, driver)
	}
	return gormInstance, nil
}

// PartitionByDatasource splits items into those that target selected and those
// that target some other configured datasource, and reports the first item whose
// declared datasource is not configured at all.
//
// The unconfigured case is an error rather than a skip: a migration naming a
// datasource nobody registered is a wiring mistake that would otherwise never run
// and never say so.
func PartitionByDatasource[T any](
	items []T,
	selected string,
	isConfigured func(name string) bool,
	describe func(item T) string,
) (matching []T, skipped []T, err error) {
	for _, item := range items {
		target := TargetDatasourceOf(item)

		if target == selected {
			matching = append(matching, item)
			continue
		}

		if !isConfigured(target) {
			return nil, nil, fmt.Errorf(
				"%s targets datasource %q, which is not configured under the `databases` key",
				describe(item), target)
		}

		skipped = append(skipped, item)
	}
	return matching, skipped, nil
}

// IsConfigured reports whether dbContext knows a datasource by that name.
func IsConfigured(dbContext core.IDBContext) func(string) bool {
	return func(name string) bool {
		return dbContext != nil && dbContext.GetDataSource(name) != nil
	}
}
