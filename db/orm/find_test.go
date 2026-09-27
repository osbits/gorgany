package orm

import (
	"context"
	"errors"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindReturnsExecutionError. Find tested the error from rendering its query where it
// meant to test the result of running it, so a query the server refused came back as a
// nil entity and no error — indistinguishable from a row that is not there. A caller that
// maps "not found" to a 404 turned outages into 404s, and one that creates the row when it
// is missing wrote duplicates.
func TestFindReturnsExecutionError(t *testing.T) {
	mockDS := NewMockDataSource()
	failure := errors.New("connection reset by peer")
	mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		return dbCore.QueryResult{Error: failure}
	}

	_, err := New[*TestEntity](mockDS.session).Find(1)

	require.Error(t, err)
	assert.True(t, errors.Is(err, failure), "the execution error must be wrapped, got %v", err)
}

// TestFindStillReportsAMissingRowAsNoError is the other half: not finding a row is an
// answer, not a failure, and callers rely on the nil entity to tell it apart.
func TestFindStillReportsAMissingRowAsNoError(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		return dbCore.QueryResult{Found: false}
	}

	entity, err := New[*TestEntity](mockDS.session).Find(1)

	require.NoError(t, err)
	assert.Nil(t, entity)
}

// TestFindRefusesACompositeKey. One id cannot address one row of a composite key: Find
// used to query the first key column alone and return whichever row the server produced
// first. It now says so, naming every key column, and sends nothing.
func TestFindRefusesACompositeKey(t *testing.T) {
	mockDS := NewMockDataSource()
	queried := false
	mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		queried = true
		return dbCore.QueryResult{}
	}

	_, err := New[*OrderLine](mockDS.session).Find(7)

	require.Error(t, err)
	assert.Equal(t, "orm: Find(id) cannot address the composite primary key of order_lines "+
		"(order_id, line_no); query by every key column", err.Error())
	assert.False(t, queried, "no query may reach the database")
}
