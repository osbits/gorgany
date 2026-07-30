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
