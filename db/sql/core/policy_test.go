package core_test

import (
	"errors"
	"fmt"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
)

// plainDataSource is a datasource written before policies existed: it implements
// IDataSource and nothing else.
type plainDataSource struct{}

func (plainDataSource) NewSession() (dbCore.ISession, error) { return nil, nil }
func (plainDataSource) GetDriver() (any, error)              { return nil, nil }
func (plainDataSource) Close() error                         { return nil }

// reportingDataSource reports the policy a test gives it. Policy has a pointer receiver
// that reads the struct, so calling it on a nil pointer would panic.
type reportingDataSource struct {
	plainDataSource
	policy dbCore.DataSourcePolicy
}

func (d *reportingDataSource) Policy() dbCore.DataSourcePolicy { return d.policy }

func TestPolicyOfAPlainDataSourceIsFullyManaged(t *testing.T) {
	assert.Equal(t, dbCore.DataSourcePolicy{}, dbCore.PolicyOf(plainDataSource{}),
		"a datasource that predates policies gets what gorgany always assumed of it")
	assert.Equal(t, dbCore.DataSourcePolicy{}, dbCore.PolicyOf(nil))
	assert.False(t, dbCore.IsExternalSchema(plainDataSource{}))
	assert.False(t, dbCore.IsReadOnly(plainDataSource{}))
}

func TestPolicyOfReportsTheImplementation(t *testing.T) {
	for _, policy := range []dbCore.DataSourcePolicy{
		{},
		{ExternalSchema: true},
		{ReadOnly: true},
		{ExternalSchema: true, ReadOnly: true},
	} {
		ds := &reportingDataSource{policy: policy}
		assert.Equal(t, policy, dbCore.PolicyOf(ds))
		assert.Equal(t, policy.ExternalSchema, dbCore.IsExternalSchema(ds))
		assert.Equal(t, policy.ReadOnly, dbCore.IsReadOnly(ds))
	}

	var typedNil *reportingDataSource
	assert.NotPanics(t, func() {
		assert.Equal(t, dbCore.DataSourcePolicy{}, dbCore.PolicyOf(typedNil),
			"a nil pointer is a nil datasource, whose Policy must not be called")
	})
}

// TestPolicyRefusalNamesTheSchemaFirst: every layer that refuses on policy takes its sentinel
// from Refusal, so external_schema is named when both flags are set, whichever layer refuses.
func TestPolicyRefusalNamesTheSchemaFirst(t *testing.T) {
	assert.NoError(t, dbCore.DataSourcePolicy{}.Refusal())
	assert.Same(t, dbCore.ErrExternalSchema, dbCore.DataSourcePolicy{ExternalSchema: true}.Refusal())
	assert.Same(t, dbCore.ErrReadOnly, dbCore.DataSourcePolicy{ReadOnly: true}.Refusal())
	assert.Same(t, dbCore.ErrExternalSchema, dbCore.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}.Refusal())
}

func TestPolicySentinelsSurviveWrapping(t *testing.T) {
	wrapped := fmt.Errorf("db:migrate --datasource=legacy: %w", dbCore.ErrExternalSchema)
	assert.ErrorIs(t, wrapped, dbCore.ErrExternalSchema)
	assert.ErrorIs(t, fmt.Errorf("outer: %w", wrapped), dbCore.ErrExternalSchema)
	assert.ErrorIs(t, errors.Join(errors.New("unrelated"), dbCore.ErrReadOnly), dbCore.ErrReadOnly)

	assert.NotErrorIs(t, dbCore.ErrReadOnly, dbCore.ErrExternalSchema, "the two refusals stay distinguishable")
	assert.NotErrorIs(t, dbCore.ErrExternalSchema, dbCore.ErrReadOnly)

	assert.ErrorIs(t, dbCore.GuardReadOnlySQL("DELETE FROM legacy", dbCore.LexiconTSQL), dbCore.ErrReadOnly)
	assert.ErrorIs(t, dbCore.GuardReadOnlyShape("DELETE FROM legacy", dbCore.LexiconTSQL), dbCore.ErrReadOnly)
	assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL("DROP TABLE legacy", dbCore.LexiconTSQL), dbCore.ErrExternalSchema)

	assert.Contains(t, dbCore.ErrReadOnly.Error(), "read_only: true", "the text names the setting to change")
	assert.Contains(t, dbCore.ErrExternalSchema.Error(), "external_schema: true")
}
