package driver

import (
	"errors"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDataSource struct{ name string }

func (f *fakeDataSource) NewSession() (dbCore.ISession, error) { return nil, nil }
func (f *fakeDataSource) GetDriver() (any, error)              { return nil, nil }
func (f *fakeDataSource) Close() error                         { return nil }

func withCleanRegistry(t *testing.T) {
	t.Helper()
	reset()
	t.Cleanup(reset)
}

func TestRegisterAndLookup(t *testing.T) {
	withCleanRegistry(t)

	Register("fake", func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &fakeDataSource{name: cfg.Database}, nil
	})

	ctor, ok := Lookup("fake")
	require.True(t, ok)
	require.NotNil(t, ctor)

	_, ok = Lookup("nope")
	assert.False(t, ok)
}

func TestNewBuildsThroughRegisteredConstructor(t *testing.T) {
	withCleanRegistry(t)

	Register("fake", func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &fakeDataSource{name: cfg.Database}, nil
	})

	ds, err := New(dsconfig.DataSource{Driver: "fake", Database: "widgets"})
	require.NoError(t, err)
	assert.Equal(t, "widgets", ds.(*fakeDataSource).name)
}

// TestNewNamesRegisteredDriversOnUnknownName is the T1.1 usability half: an
// unknown driver used to fall through the hard-coded switch to a silent nil.
func TestNewNamesRegisteredDriversOnUnknownName(t *testing.T) {
	withCleanRegistry(t)

	Register("postgres_gorm", func(dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &fakeDataSource{}, nil
	})
	Register("mysql_gorm", func(dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &fakeDataSource{}, nil
	})

	_, err := New(dsconfig.DataSource{Driver: "oracle_gorm", Database: "d"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown driver "oracle_gorm"`)
	assert.Contains(t, err.Error(), "mysql_gorm")
	assert.Contains(t, err.Error(), "postgres_gorm")
}

func TestNewRejectsEmptyDriver(t *testing.T) {
	withCleanRegistry(t)

	_, err := New(dsconfig.DataSource{Database: "d"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'driver' is required")
}

func TestNewPropagatesConstructorError(t *testing.T) {
	withCleanRegistry(t)

	sentinel := errors.New("cannot dial")
	Register("fake", func(dsconfig.DataSource) (dbCore.IDataSource, error) {
		return nil, sentinel
	})

	_, err := New(dsconfig.DataSource{Driver: "fake", Database: "d"})
	require.ErrorIs(t, err, sentinel)
}

func TestNewRejectsNilDataSourceFromConstructor(t *testing.T) {
	withCleanRegistry(t)

	Register("fake", func(dsconfig.DataSource) (dbCore.IDataSource, error) {
		return nil, nil
	})

	_, err := New(dsconfig.DataSource{Driver: "fake", Database: "d"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil datasource")
}

// TestRegisterRejectsDuplicates: two drivers answering to one config value is a
// wiring mistake with no correct resolution, and silently keeping one of them is
// how the pre-v2 provider ended up nondeterministic.
func TestRegisterRejectsDuplicates(t *testing.T) {
	withCleanRegistry(t)

	ctor := func(dsconfig.DataSource) (dbCore.IDataSource, error) { return &fakeDataSource{}, nil }
	Register("fake", ctor)

	assert.PanicsWithValue(t,
		`gorgany/db/sql/driver: driver "fake" is already registered`,
		func() { Register("fake", ctor) })
}

func TestRegisterRejectsEmptyNameAndNilConstructor(t *testing.T) {
	withCleanRegistry(t)

	assert.Panics(t, func() {
		Register("", func(dsconfig.DataSource) (dbCore.IDataSource, error) { return nil, nil })
	})
	assert.Panics(t, func() { Register("x", nil) })
}

func TestNamesIsSorted(t *testing.T) {
	withCleanRegistry(t)

	ctor := func(dsconfig.DataSource) (dbCore.IDataSource, error) { return &fakeDataSource{}, nil }
	Register("zeta", ctor)
	Register("alpha", ctor)
	Register("mid", ctor)

	assert.Equal(t, []string{"alpha", "mid", "zeta"}, Names())
}

// policyDataSource is a datasource that reports a fixed policy and counts its closes.
type policyDataSource struct {
	fakeDataSource
	policy dbCore.DataSourcePolicy
	closed int
}

func (p *policyDataSource) Policy() dbCore.DataSourcePolicy { return p.policy }
func (p *policyDataSource) Close() error                    { p.closed++; return nil }

// closingDataSource is a datasource that knows no policy and counts its closes, as one an app
// wrote before the flags existed does.
type closingDataSource struct {
	fakeDataSource
	closed int
}

func (c *closingDataSource) Close() error { c.closed++; return nil }

// TestNewRefusesADriverThatIgnoresAPolicyFlag. A constructor written before external_schema and
// read_only existed ignores them, and its datasource reports the zero policy, so without this
// check it would boot as one gorgany owns and may write to, and every later check that asks
// core.PolicyOf would agree.
func TestNewRefusesADriverThatIgnoresAPolicyFlag(t *testing.T) {
	for _, c := range []struct {
		name    string
		cfg     dsconfig.DataSource
		ignored string
	}{
		{"read_only", dsconfig.DataSource{ReadOnly: true}, "read_only"},
		{"external_schema", dsconfig.DataSource{ExternalSchema: true}, "external_schema"},
		{"both", dsconfig.DataSource{ReadOnly: true, ExternalSchema: true}, "external_schema and read_only"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withCleanRegistry(t)

			built := &closingDataSource{}
			Register("app_engine", func(dsconfig.DataSource) (dbCore.IDataSource, error) { return built, nil })

			c.cfg.Driver, c.cfg.Database = "app_engine", "legacy"
			ds, err := New(c.cfg)
			require.Error(t, err)
			assert.Nil(t, ds)

			assert.True(t, dbCore.IsUnsupported(err), "a refused setting is an UnsupportedError, as on Postgres and MySQL: %v", err)
			assert.Contains(t, err.Error(), `driver "app_engine" does not support `+c.ignored+";")
			assert.Contains(t, err.Error(), "core.PolicyReporter")
			assert.Equal(t, 1, built.closed, "the datasource the caller never receives must be closed")
		})
	}
}

// TestNewRefusesADriverThatReportsLessThanItsConfigSets: reporting one flag does not excuse
// ignoring the other.
func TestNewRefusesADriverThatReportsLessThanItsConfigSets(t *testing.T) {
	withCleanRegistry(t)

	built := &policyDataSource{policy: dbCore.DataSourcePolicy{ExternalSchema: true}}
	Register("app_engine", func(dsconfig.DataSource) (dbCore.IDataSource, error) { return built, nil })

	_, err := New(dsconfig.DataSource{Driver: "app_engine", Database: "legacy", ExternalSchema: true, ReadOnly: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support read_only;")
	assert.NotContains(t, err.Error(), "external_schema", "the flag it reports is not the problem")
	assert.Equal(t, 1, built.closed)
}

// TestNewAcceptsADriverThatReportsItsFlags, and one that knows no policy when its config sets
// none, which is every driver written before the flags existed.
func TestNewAcceptsADriverThatReportsItsFlags(t *testing.T) {
	withCleanRegistry(t)

	enforcing := &policyDataSource{policy: dbCore.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}}
	Register("enforcing", func(dsconfig.DataSource) (dbCore.IDataSource, error) { return enforcing, nil })
	plain := &closingDataSource{}
	Register("plain", func(dsconfig.DataSource) (dbCore.IDataSource, error) { return plain, nil })

	ds, err := New(dsconfig.DataSource{Driver: "enforcing", Database: "legacy", ExternalSchema: true, ReadOnly: true})
	require.NoError(t, err)
	assert.Same(t, enforcing, ds)
	assert.Zero(t, enforcing.closed)

	ds, err = New(dsconfig.DataSource{Driver: "plain", Database: "legacy"})
	require.NoError(t, err)
	assert.Same(t, plain, ds)
	assert.Zero(t, plain.closed)
}
