package db

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
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
//
// It does not ask what the datasource's configuration allows. A command that changes a
// schema or records anything resolves through ResolveOwnedGorm instead.
func ResolveGorm(dbContext core.IDBContext, name string) (*gorm.DB, error) {
	dataSource, err := configuredDataSource(dbContext, name)
	if err != nil {
		return nil, err
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

// configuredDataSource returns the named datasource, or the error ResolveGorm has always
// given for a missing context or an unknown name. RequireOwnedDatasource shares it, so a
// typo in --datasource reads the same whichever of the two a command calls.
func configuredDataSource(dbContext core.IDBContext, name string) (dbCore.IDataSource, error) {
	if dbContext == nil {
		return nil, fmt.Errorf("no database context available")
	}

	dataSource := dbContext.GetDataSource(name)
	if dataSource == nil {
		return nil, fmt.Errorf(
			"datasource %q is not configured; check the `databases` config key and the --%s flag",
			name, DatasourceFlag)
	}
	return dataSource, nil
}

// RequireOwnedDatasource reports whether command may change the schema of, and keep its
// bookkeeping in, the named datasource. It returns nil when it may.
//
// db:migrate, db:seed and db:diff used to start with AutoMigrate or DDL on whatever
// datasource was selected. On a datasource whose schema another system owns, that created
// a `migrations` or `seeders` table in someone else's database or, where the connection's
// DDL guard caught it, failed the run at a statement that says nothing about which setting
// to change. So the refusal now comes from the configuration, before a statement is sent.
// It is a configuration error, like an unknown datasource, and the commands panic with it
// (exit 2).
//
// external_schema: true is refused with an error wrapping dbCore.ErrExternalSchema, and
// read_only: true with one wrapping dbCore.ErrReadOnly: the rows db:migrate and db:seed
// record, and the DDL db:diff runs and rolls back, are all writes. When both are set,
// external_schema is named (see dbCore.DataSourcePolicy.Refusal). A missing context or an
// unknown name gets exactly ResolveGorm's error.
func RequireOwnedDatasource(dbContext core.IDBContext, name, command string) error {
	dataSource, err := configuredDataSource(dbContext, name)
	if err != nil {
		return err
	}

	refusal := dbCore.PolicyOf(dataSource).Refusal()
	var message string
	switch refusal {
	case nil:
		return nil
	case dbCore.ErrExternalSchema:
		message = fmt.Sprintf(
			"%s refuses datasource %q: its schema is owned outside gorgany (external_schema: true) — "+
				"gorgany never creates, alters or records anything there, not even its own "+
				"migrations/seeders tables; select a datasource gorgany owns with --%s, or remove "+
				"external_schema from databases.%s if gorgany owns that schema after all",
			command, name, DatasourceFlag, name)
	default:
		message = fmt.Sprintf(
			"%s refuses datasource %q: it is read-only (read_only: true) — gorgany writes nothing "+
				"there, not even its own migrations/seeders tables; select a writable datasource "+
				"with --%s, or remove read_only from databases.%s if it should accept writes",
			command, name, DatasourceFlag, name)
	}
	return &policyRefusal{sentinel: refusal, message: message}
}

// ResolveOwnedGorm is ResolveGorm for a command that changes the schema of, or keeps its
// bookkeeping in, the datasource. It asks RequireOwnedDatasource first, so an unowned
// datasource is refused on its configuration alone, whether or not its driver is GORM.
func ResolveOwnedGorm(dbContext core.IDBContext, name, command string) (*gorm.DB, error) {
	if err := RequireOwnedDatasource(dbContext, name, command); err != nil {
		return nil, err
	}
	return ResolveGorm(dbContext, name)
}

// policyRefusal is a refusal that reads as one sentence about the datasource and still
// unwraps to the policy sentinel. Wrapping the sentinel with %w would splice its own text
// ("datasource schema is owned outside gorgany …") into the middle of that sentence;
// errors.Is only needs Unwrap.
type policyRefusal struct {
	sentinel error
	message  string
}

func (e *policyRefusal) Error() string { return e.message }
func (e *policyRefusal) Unwrap() error { return e.sentinel }

// refuseUnownedTargets reports the first item that targets an external_schema or
// read_only datasource, whether or not that datasource is the one selected.
//
// Such an item can never run: every run on its datasource is refused. Skipping it quietly
// on the other datasources' runs would leave a migration that is registered, never
// applied and never mentioned, which is the same wiring mistake PartitionByDatasource
// refuses for a datasource that is not configured. An item whose datasource is not
// configured passes here and is left to PartitionByDatasource.
func refuseUnownedTargets[T any](items []T, dbContext core.IDBContext, describe func(item T) string) error {
	if dbContext == nil {
		return nil
	}

	for _, item := range items {
		target := TargetDatasourceOf(item)

		var message string
		refusal := dbCore.PolicyOf(dbContext.GetDataSource(target)).Refusal()
		switch refusal {
		case nil:
			continue
		case dbCore.ErrExternalSchema:
			message = fmt.Sprintf(
				"%s targets datasource %q, which is external_schema: true; gorgany runs no "+
					"migration or seeder on a datasource whose schema it does not own, and keeps no "+
					"migrations/seeders table there — delete it or retarget it at a datasource gorgany owns",
				describe(item), target)
		default:
			message = fmt.Sprintf(
				"%s targets datasource %q, which is read_only: true; gorgany runs no migration or "+
					"seeder on a datasource it may not write to, and keeps no migrations/seeders table "+
					"there — delete it or retarget it at a writable datasource",
				describe(item), target)
		}
		return &policyRefusal{sentinel: refusal, message: message}
	}
	return nil
}

// partitionOwned is PartitionByDatasource followed by refuseUnownedTargets over every item,
// matching and skipped alike, so db:migrate and db:seed refuse an item aimed at an unowned
// datasource on every run, not only on the run that selects it.
func partitionOwned[T any](
	items []T,
	selected string,
	dbContext core.IDBContext,
	describe func(item T) string,
) (matching []T, skipped []T, err error) {
	matching, skipped, err = PartitionByDatasource(items, selected, IsConfigured(dbContext), describe)
	if err != nil {
		return nil, nil, err
	}
	if err := refuseUnownedTargets(items, dbContext, describe); err != nil {
		return nil, nil, err
	}
	return matching, skipped, nil
}

// DomainDatasourceOf returns the datasource a registered domain belongs to: what its
// DbConnectionName reports (core.DbConnectionNamer), else what its DataSourceName reports
// (DatasourceScoped), else core.DefaultKeyInRegistrar. A blank name counts as none, as it
// does for TargetDatasourceOf.
//
// db:diff used to diff every registered domain against the selected datasource, so a
// second datasource's tables were proposed as new tables for the first. It diffs only the
// domains this names.
//
// Domains are registered as values or as pointers, and either may declare the method on
// a pointer receiver, which a value's method set lacks. So a pointer to a copy of the
// registered value is asked as well as the value itself, and a pointer to a new value is
// the only one asked when the registered value is a nil pointer, which a value-receiver
// method would dereference.
//
// Nothing asked a domain these methods before db:diff did, so one may panic when asked —
// promoted through a nil embedded pointer, say. That is returned as an error naming the
// method, rather than crashing the command with a nil dereference that names no domain.
func DomainDatasourceOf(model any) (string, error) {
	name, declared, err := declaredDomainDatasource(model)
	if err != nil {
		return "", err
	}
	if !declared {
		return core.DefaultKeyInRegistrar, nil
	}
	return name, nil
}

// declaredDomainDatasource returns the datasource model declares, as DomainDatasourceOf
// reads it, and false when it declares none. db:diff needs to tell a domain that declares
// nothing from one that names `default`: in an app with no `default`, the first is diffed
// against the selected datasource, and the second is a wiring mistake.
func declaredDomainDatasource(model any) (name string, declared bool, err error) {
	candidates := domainCandidates(model)

	for _, candidate := range candidates {
		if namer, ok := candidate.(core.DbConnectionNamer); ok {
			if name, err := askDomain("DbConnectionName", namer.DbConnectionName); err != nil || name != "" {
				return name, err == nil, err
			}
		}
	}
	for _, candidate := range candidates {
		if scoped, ok := candidate.(DatasourceScoped); ok {
			if name, err := askDomain("DataSourceName", scoped.DataSourceName); err != nil || name != "" {
				return name, err == nil, err
			}
		}
	}
	return "", false, nil
}

// askDomain calls one of a domain's naming methods and returns its answer, trimmed, or the
// panic it raised as an error.
func askDomain(method string, call func() string) (name string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("its %s() panicked: %v", method, recovered)
		}
	}()
	return strings.TrimSpace(call()), nil
}

// domainCandidates returns the values DomainDatasourceOf asks: model itself unless it is a
// nil pointer, then a pointer to a copy of the struct model holds, or to a new one when
// model is a nil pointer.
func domainCandidates(model any) []any {
	if model == nil {
		return nil
	}

	var candidates []any
	value := reflect.ValueOf(model)
	if value.Kind() != reflect.Pointer || !value.IsNil() {
		candidates = append(candidates, model)
	}

	for value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	t := value.Type()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	copied := reflect.New(t)
	if value.Kind() != reflect.Pointer {
		// The registered state, so the pointer's methods read what the value's would.
		copied.Elem().Set(value)
	}
	return append(candidates, copied.Interface())
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
