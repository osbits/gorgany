package core_test

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
)

// capabilityDialect declares only what a test gives it. The embedded SQLDialect is nil: the
// probes must answer from the optional interfaces alone, never by rendering anything.
type capabilityDialect struct{ dbCore.SQLDialect }

type bindLimitedDialect struct {
	capabilityDialect
	max int
}

func (d bindLimitedDialect) MaxBindParameters() int { return d.max }

type triggerSensitiveDialect struct {
	capabilityDialect
	blocked bool
}

func (d triggerSensitiveDialect) ReturningBlockedByTriggers() bool { return d.blocked }

func TestBindParameterLimit(t *testing.T) {
	assert.Zero(t, dbCore.BindParameterLimit(nil), "a nil dialect declares nothing")
	assert.Zero(t, dbCore.BindParameterLimit(capabilityDialect{}), "a dialect that does not say has no limit")
	assert.Equal(t, 2098, dbCore.BindParameterLimit(bindLimitedDialect{max: 2098}), "what SQL Server leaves a statement once sp_executesql has taken two of its 2100")
	assert.Zero(t, dbCore.BindParameterLimit(bindLimitedDialect{max: 0}), "zero is no limit")
	assert.Zero(t, dbCore.BindParameterLimit(bindLimitedDialect{max: -1}), "a negative limit is no limit, not a refusal of every statement")
}

func TestReturningBlockedByTriggers(t *testing.T) {
	assert.False(t, dbCore.ReturningBlockedByTriggers(nil), "a nil dialect declares nothing")
	assert.False(t, dbCore.ReturningBlockedByTriggers(capabilityDialect{}), "a dialect that does not say is not blocked")
	assert.True(t, dbCore.ReturningBlockedByTriggers(triggerSensitiveDialect{blocked: true}))
	assert.False(t, dbCore.ReturningBlockedByTriggers(triggerSensitiveDialect{blocked: false}))
}
