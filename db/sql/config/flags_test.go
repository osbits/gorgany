package config_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ownership and access flags are the keys a misparse would hurt most. external_schema and
// read_only exist to stop gorgany writing where it must not, so a spelling of `true` that
// parsed as false would switch the protection off without a word; a value that is not a
// boolean at all has to stop the boot for the same reason.

// flagKeys are the boolean datasource keys this file covers.
var flagKeys = []string{"external_schema", "read_only", "lazy_connect"}

func flagsOf(cfg config.DataSource) map[string]bool {
	return map[string]bool{
		"external_schema": cfg.ExternalSchema,
		"read_only":       cfg.ReadOnly,
		"lazy_connect":    cfg.LazyConnect,
	}
}

func TestParseReadsTheOwnershipAndAccessFlags(t *testing.T) {
	raw := validRaw()
	for _, key := range flagKeys {
		raw[key] = true
	}

	cfg, err := config.Parse(raw)
	require.NoError(t, err)

	for key, value := range flagsOf(cfg) {
		assert.Truef(t, value, "%s: true must reach the DataSource", key)
	}
}

// TestTheOwnershipAndAccessFlagsDefaultToOff. Off is today's behaviour, so an existing config
// that never mentions the keys must keep it.
func TestTheOwnershipAndAccessFlagsDefaultToOff(t *testing.T) {
	cfg, err := config.Parse(validRaw())
	require.NoError(t, err)

	for key, value := range flagsOf(cfg) {
		assert.Falsef(t, value, "%s must default to off", key)
	}
}

// TestTheFlagsAcceptViperStringForms. YAML gives a bool, but an environment override or a
// quoted YAML value gives a string, and a flag set from `READ_ONLY=true` must mean the same as
// one written in the file.
func TestTheFlagsAcceptViperStringForms(t *testing.T) {
	forms := map[string]bool{
		"true": true, "TRUE": true, " true ": true, "1": true,
		"false": false, "False": false, "0": false,
	}

	for _, key := range flagKeys {
		for form, want := range forms {
			t.Run(key+"="+form, func(t *testing.T) {
				raw := validRaw()
				raw[key] = form

				cfg, err := config.Parse(raw)
				require.NoError(t, err)
				assert.Equal(t, want, flagsOf(cfg)[key])
			})
		}
	}
}

// TestTheFlagsRejectNonBooleans, including the empty string. That is what an unresolved
// ${READ_ONLY} placeholder boots as, and reading it as false would turn a variable missing from
// one deployment into writes that deployment was configured to refuse.
func TestTheFlagsRejectNonBooleans(t *testing.T) {
	for _, key := range flagKeys {
		for _, value := range []any{7, "yes-please", "", []string{"true"}} {
			raw := validRaw()
			raw[key] = value

			_, err := config.Parse(raw)
			require.Errorf(t, err, "%s: %#v must be refused", key, value)
			assert.Contains(t, err.Error(), "'"+key+"'")
			assert.Contains(t, err.Error(), "boolean")
		}
	}
}

func TestParseReadsInstanceAndLazyConnect(t *testing.T) {
	raw := validRaw()
	raw["instance"] = "legacy"
	raw["lazy_connect"] = true

	cfg, err := config.Parse(raw)
	require.NoError(t, err)

	assert.Equal(t, "legacy", cfg.Instance)
	assert.True(t, cfg.LazyConnect)
}

func TestAnAbsentInstanceIsEmpty(t *testing.T) {
	cfg, err := config.Parse(validRaw())
	require.NoError(t, err)
	assert.Empty(t, cfg.Instance)
}

func TestInstanceMustBeAString(t *testing.T) {
	raw := validRaw()
	raw["instance"] = []string{"legacy"}

	_, err := config.Parse(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'instance'")
	assert.Contains(t, err.Error(), "string")
}
