package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBuilderSpeaksSQLServer(t *testing.T) {
	b := NewBuilder()
	require.IsType(t, &SQLServerDialect{}, b.Dialect())
	assert.False(t, b.Dialect().(*SQLServerDialect).ReadOnly, "NewBuilder is for a writable datasource")

	sql, _ := render(t, b.Select("Id").From("dbo.2024Orders").Limit(1))
	assert.Equal(t, "SELECT TOP (1) [Id] FROM [dbo].[2024Orders]", sql)
}

func TestNewBuilderWithDialect(t *testing.T) {
	ro := &SQLServerDialect{ReadOnly: true}
	b := NewBuilderWithDialect(ro)
	assert.Same(t, ro, b.Dialect())

	_, _, err := b.Delete("t").ToSQL()
	assert.ErrorIs(t, err, dbCore.ErrReadOnly)
}
