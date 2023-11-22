package postgres

import (
	"github.com/stretchr/testify/assert"
	"gorgany/app/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
)

func getBuilder() core.IQueryBuilder {
	db, _ := gorm.Open(postgres.New(postgres.Config{}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	return NewBuilder(&GormPostgresConnection{gormInstance: db})
}

func TestBuilder_From(t *testing.T) {
	builder := getBuilder()

	query := builder.From("test_table").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM test_table", query)
}

func TestBuilder_From_Empty(t *testing.T) {
	builder := getBuilder()

	query := builder.From("").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM ", query)
}

func TestBuilder_Select_Star(t *testing.T) {
	builder := getBuilder()

	query := builder.Select("*").From("test_table").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM test_table", query)
}

func TestBuilder_Select_ConcreteFields(t *testing.T) {
	builder := getBuilder()

	query := builder.Select("col_1", "col_2").From("test_table").ToProcessedQuery()

	assert.Equal(t, "SELECT col_1, col_2 FROM test_table", query)
}

func TestBuilder_Join(t *testing.T) {
	builder := getBuilder()

	query := builder.From("test_table tt").Join("joinable_table jt", "tt.id", "=", "jt.test_table_id").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM test_table tt INNER JOIN joinable_table jt ON tt.id = jt.test_table_id", query)
}

func TestBuilder_Join_Multiples(t *testing.T) {
	builder := getBuilder()

	query := builder.From("test_table tt").Join("joinable_table jt", "tt.id", "=", "jt.test_table_id").Join("joinable_table_2 jt2", "jt2.joinable_table_id", "=", "jt.id").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM test_table tt INNER JOIN joinable_table jt ON tt.id = jt.test_table_id, joinable_table_2 jt2 ON jt2.joinable_table_id = jt.id", query)
}

func TestBuilder_Join_WithRawSubquery(t *testing.T) {
	builder := getBuilder()

	query := builder.From("test_table tt").Join(NewRaw("SELECT * FROM joinable_table WHERE id in (1, 2, 3, 4)", "jt"), "tt.id", "=", "jt.test_id").ToProcessedQuery()

	assert.Equal(t, "SELECT * FROM test_table tt INNER JOIN (SELECT * FROM joinable_table WHERE id in (1, 2, 3, 4)) jt ON tt.id = jt.test_id", query)
}
