package util

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The primitive resolvers turn raw path/query strings into typed values, so every
// input here is caller-controlled. FloatResolver asserted the result of
// ConvertReflectedValue unchecked; the assertion holds today, but a resolver on a
// request path should degrade to a parse error rather than a panic.

func TestFloatResolverParsesValidInput(t *testing.T) {
	tests := map[reflect.Kind]struct {
		in   string
		want any
	}{
		reflect.Float64: {in: "1.5", want: float64(1.5)},
		reflect.Float32: {in: "2.5", want: float32(2.5)},
	}

	for kind, tc := range tests {
		got, err := FloatResolver{}.Resolve(kind, tc.in)
		require.NoErrorf(t, err, "kind %s", kind)
		assert.Equalf(t, tc.want, got, "kind %s", kind)
	}
}

func TestFloatResolverRejectsGarbageWithoutPanicking(t *testing.T) {
	for _, in := range []string{"", "abc", "1.2.3", "0x1p", "  1.5  "} {
		var err error
		require.NotPanicsf(t, func() {
			_, err = FloatResolver{}.Resolve(reflect.Float64, in)
		}, "input %q", in)
		require.Errorf(t, err, "input %q must be a parse error", in)
	}
}

// TestResolvePrimitiveNeverPanicsOnGarbage sweeps every kind the resolver table
// covers with inputs no client is stopped from sending.
func TestResolvePrimitiveNeverPanicsOnGarbage(t *testing.T) {
	kinds := []reflect.Kind{
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Bool, reflect.String,
	}
	inputs := []string{"", "abc", "-1", "999999999999999999999999", "1.5", "NaN", "true"}

	for _, kind := range kinds {
		for _, in := range inputs {
			require.NotPanicsf(t, func() {
				_, _ = ResolvePrimitive(kind, in)
			}, "kind %s input %q", kind, in)
		}
	}
}

func TestFloatResolverHandlesTheExtremes(t *testing.T) {
	got, err := FloatResolver{}.Resolve(reflect.Float64, "1e308")
	require.NoError(t, err)
	assert.Equal(t, 1e308, got)

	_, err = FloatResolver{}.Resolve(reflect.Float64, "1e999")
	require.Error(t, err, "an out-of-range literal is a parse error")

	got, err = FloatResolver{}.Resolve(reflect.Float64, "-0")
	require.NoError(t, err)
	assert.Equal(t, math.Copysign(0, -1), got)
}
