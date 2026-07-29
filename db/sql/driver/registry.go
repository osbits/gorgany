// Package driver holds the datasource driver registry: a mapping from a driver
// name as written in the app config (`postgres_gorm`, `mysql_gorm`, ...) to the
// constructor that builds that datasource.
//
// Before v2 the provider had a hard-coded switch that understood exactly one
// driver, so adding an engine meant editing the framework. Registration also
// makes an unknown driver name fail at boot with the list of names that do exist,
// instead of silently registering nothing.
package driver

import (
	"fmt"
	"sort"
	"sync"

	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
)

// Constructor builds a datasource from a validated typed config.
type Constructor func(cfg dsconfig.DataSource) (dbCore.IDataSource, error)

var (
	mu      sync.RWMutex
	drivers = make(map[string]Constructor)
)

// Register associates a driver name with its constructor.
//
// Registering the same name twice panics: two drivers answering to one config
// value is a wiring mistake with no correct resolution, and silently keeping one
// of them is how the pre-v2 provider ended up nondeterministic.
func Register(name string, ctor Constructor) {
	if name == "" {
		panic("gorgany/db/sql/driver: Register requires a non-empty driver name")
	}
	if ctor == nil {
		panic(fmt.Sprintf("gorgany/db/sql/driver: Register(%q) requires a non-nil constructor", name))
	}

	mu.Lock()
	defer mu.Unlock()

	if _, exists := drivers[name]; exists {
		panic(fmt.Sprintf("gorgany/db/sql/driver: driver %q is already registered", name))
	}
	drivers[name] = ctor
}

// Lookup returns the constructor registered for name.
func Lookup(name string) (Constructor, bool) {
	mu.RLock()
	defer mu.RUnlock()
	ctor, ok := drivers[name]
	return ctor, ok
}

// Names returns every registered driver name, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()

	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// New builds the datasource for cfg.Driver.
//
// An unregistered driver is an error naming what is available, rather than a
// silently skipped connection.
func New(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
	if cfg.Driver == "" {
		return nil, fmt.Errorf("datasource config: 'driver' is required (registered drivers: %v)", Names())
	}

	ctor, ok := Lookup(cfg.Driver)
	if !ok {
		return nil, fmt.Errorf("datasource config: unknown driver %q (registered drivers: %v)", cfg.Driver, Names())
	}

	ds, err := ctor(cfg)
	if err != nil {
		return nil, err
	}
	if ds == nil {
		return nil, fmt.Errorf("datasource config: driver %q returned a nil datasource", cfg.Driver)
	}
	return ds, nil
}

// reset clears the registry. Test-only.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	drivers = make(map[string]Constructor)
}
