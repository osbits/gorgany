package decoder

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexedCollectionsDecode is the happy path: `items[0][name]` style keys become
// a slice of maps.
func TestIndexedCollectionsDecode(t *testing.T) {
	params, err := ParseUrlValues(url.Values{
		"items[0][name]":  {"first"},
		"items[0][price]": {"10"},
		"items[1][name]":  {"second"},
		"plain":           {"value"},
	})

	require.NoError(t, err)
	assert.Equal(t, "value", params.GetString("plain"))
	assert.Equal(t, []map[string]string{
		{"name": "first", "price": "10"},
		{"name": "second"},
	}, params.GetArrayMap("items"))
}

// TestARepeatedIndexedParameterIsAnErrorNotAPanic covers the B1 site the code itself
// had flagged with `// todo: Test it`.
//
// A repeated key gives url.Values a []string, so the value is not a string and the
// unchecked `value.(string)` panicked. The query string is entirely caller-controlled,
// so any client could crash the handler with `?items[0][name]=a&items[0][name]=b`.
func TestARepeatedIndexedParameterIsAnErrorNotAPanic(t *testing.T) {
	var params QueryParams
	var err error

	require.NotPanics(t, func() {
		params, err = ParseUrlValues(url.Values{
			"items[0][name]": {"first", "again"},
		})
	})

	require.Error(t, err)
	assert.Nil(t, params)
	assert.Contains(t, err.Error(), "items")
	assert.Contains(t, err.Error(), "must be a string")
}

// TestAScalarCollidingWithAnIndexedParameterNeverPanics covers the other two
// assertions in the same block: `items=x` decodes to a string, and `items[0][name]=y`
// then found a string where it expected []map[string]string.
//
// Go randomises map iteration, so which of the two keys lands first is not
// deterministic — which is exactly why this had to be an error rather than a panic.
// Both orders are asserted to be survivable: either the collision is reported, or the
// later scalar simply wins.
func TestAScalarCollidingWithAnIndexedParameterNeverPanics(t *testing.T) {
	for i := 0; i < 50; i++ {
		var err error
		require.NotPanics(t, func() {
			_, err = ParseUrlValues(url.Values{
				"items":          {"scalar"},
				"items[0][name]": {"first"},
			})
		})
		if err != nil {
			assert.Contains(t, err.Error(), "items")
			assert.Contains(t, err.Error(), "indexed collection")
		}
	}
}

// TestEmptyValuesAreDropped documents existing behaviour that the fix preserves.
func TestEmptyValuesAreDropped(t *testing.T) {
	params, err := ParseUrlValues(url.Values{
		"blank":           {""},
		"items[0][name]":  {""},
		"items[0][price]": {"10"},
	})

	require.NoError(t, err)
	_, present := params["blank"]
	assert.False(t, present, "an empty scalar is omitted")
	assert.Equal(t, []map[string]string{{"price": "10"}}, params.GetArrayMap("items"))
}

// TestAccessorsAreTypeSafe: the Get* helpers were already comma-ok, and must stay so.
func TestAccessorsAreTypeSafe(t *testing.T) {
	params := QueryParams{"scalar": "text", "list": []string{"a"}}

	assert.Equal(t, "", params.GetString("list"), "wrong type yields the zero value")
	assert.Equal(t, []string{}, params.GetArray("scalar"))
	assert.Equal(t, []map[string]string{}, params.GetArrayMap("missing"))
	assert.Equal(t, "text", params.GetString("scalar"))
}
