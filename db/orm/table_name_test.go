package orm

import (
	"context"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All, Count and Preload's loads have no entity to ask, so they ask `var sample T` — a nil
// *T for a pointer model — which GetTableName used to answer from the naming strategy
// without calling TableName(). A model whose table is not its type's default name was then
// read and counted from a table that does not exist, while Find, which asks gorm's schema,
// read the right one. The docs/app-template models happen to use TableName values equal to
// the default, which is how this went unnoticed.

// TestGetTableNameHonoursTableNameOnNilPointer covers both receivers: a pointer receiver
// is callable on the zero value behind a fresh pointer, and a value receiver on the zero
// value itself — neither may be called through the nil pointer.
func TestGetTableNameHonoursTableNameOnNilPointer(t *testing.T) {
	var pointerReceiver *TestEntityWithCustomTableName
	var valueReceiver *TestEntityWithValueReceiverTableName
	var noMethod *TestEntity

	assert.Equal(t, "custom_table_name", GetTableName(pointerReceiver))
	assert.Equal(t, "value_receiver_table", GetTableName(valueReceiver))
	assert.Equal(t, "test_entities", GetTableName(noMethod),
		"a type without TableName still gets the naming strategy's name")
	assert.Equal(t, "", GetTableName(nil), "an untyped nil names no table")
}

// TestGetTableNameHonoursTableNameOnZeroValue. A zero value passed by value was named by
// the naming strategy too, and a non-zero value whose TableName has a pointer receiver
// never reached the method, since a value does not carry its pointer methods.
func TestGetTableNameHonoursTableNameOnZeroValue(t *testing.T) {
	assert.Equal(t, "value_receiver_table", GetTableName(TestEntityWithValueReceiverTableName{}))
	assert.Equal(t, "custom_table_name", GetTableName(TestEntityWithCustomTableName{}))
	assert.Equal(t, "custom_table_name", GetTableName(TestEntityWithCustomTableName{ID: 1}),
		"a pointer-receiver TableName is honoured for a value as well")
	assert.Equal(t, "test_entities", GetTableName(TestEntity{}))
}

// TestAllUsesTableNameMethod pins the table All reads. It named the table from `var sample
// T`, a nil *T for a pointer model, so it read test_entity_with_custom_table_names, a table
// that does not exist, while Find read custom_table_name.
func TestAllUsesTableNameMethod(t *testing.T) {
	t.Run("pointer receiver", func(t *testing.T) {
		assert.Equal(t, "SELECT * FROM custom_table_name",
			capturedAllSQL(t, func(session dbCore.ISession) error {
				_, err := New[*TestEntityWithCustomTableName](session).All()
				return err
			}))
	})
	t.Run("value receiver", func(t *testing.T) {
		assert.Equal(t, "SELECT * FROM value_receiver_table",
			capturedAllSQL(t, func(session dbCore.ISession) error {
				_, err := New[*TestEntityWithValueReceiverTableName](session).All()
				return err
			}))
	})
}

// TestCountUsesTableNameMethod is TestAllUsesTableNameMethod for Count, which named its
// table the same way and so counted the rows of the naming strategy's table instead of the
// model's.
func TestCountUsesTableNameMethod(t *testing.T) {
	cases := []struct {
		name  string
		count func(dbCore.ISession) (int64, error)
		want  string
	}{
		{
			name: "pointer receiver",
			count: func(session dbCore.ISession) (int64, error) {
				return New[*TestEntityWithCustomTableName](session).Count()
			},
			want: "SELECT COUNT(*) FROM custom_table_name",
		},
		{
			name: "value receiver",
			count: func(session dbCore.ISession) (int64, error) {
				return New[*TestEntityWithValueReceiverTableName](session).Count()
			},
			want: "SELECT COUNT(*) FROM value_receiver_table",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			var sql string
			mockDS.session.executor.countRawFunc = func(_ context.Context, query string, _ ...interface{}) (int64, error) {
				sql = query
				return 4, nil
			}

			total, err := tc.count(mockDS.session)
			require.NoError(t, err)
			assert.Equal(t, int64(4), total)
			assert.Equal(t, tc.want, sql)
		})
	}
}

// capturedAllSQL runs call against a mock session and returns the SQL it sent.
func capturedAllSQL(t *testing.T, call func(dbCore.ISession) error) string {
	t.Helper()
	mockDS := NewMockDataSource()
	var sql string
	mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, query string, _ ...interface{}) dbCore.QueryResult {
		sql = query
		return dbCore.QueryResult{}
	}
	require.NoError(t, call(mockDS.session))
	return sql
}
