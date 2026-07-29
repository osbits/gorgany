package http

import (
	"mime/multipart"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withUploadConfig(t *testing.T, values map[string]any) {
	t.Helper()

	keys := []string{ConfigMaxMultipartSize, ConfigMaxFiles, ConfigMaxFileSize}
	previous := make(map[string]any, len(keys))
	for _, key := range keys {
		previous[key] = viper.Get(key)
		viper.Set(key, nil)
	}

	for key, value := range values {
		viper.Set(key, value)
	}

	t.Cleanup(func() {
		for key, value := range previous {
			viper.Set(key, value)
		}
	})
}

// TestUploadLimitsDefaultToThePreV2Constants — the limits were compile-time
// constants (32MB / 100 files / 10MB), so an app needing an 11 MB upload had to
// read Request().BodyReader by hand. Making them configurable must not change what
// an app that configures nothing gets.
func TestUploadLimitsDefaultToThePreV2Constants(t *testing.T) {
	withUploadConfig(t, nil)

	limits := resolveUploadLimits()

	assert.Equal(t, int64(32*1024*1024), limits.MaxMultipartSize)
	assert.Equal(t, 100, limits.MaxFiles)
	assert.Equal(t, int64(10*1024*1024), limits.MaxFileSize)
}

func TestUploadLimitsAreConfigurable(t *testing.T) {
	withUploadConfig(t, map[string]any{
		ConfigMaxMultipartSize: 64 * 1024 * 1024,
		ConfigMaxFiles:         5,
		ConfigMaxFileSize:      11 * 1024 * 1024, // the 11 MB upload from the brief
	})

	limits := resolveUploadLimits()

	assert.Equal(t, int64(64*1024*1024), limits.MaxMultipartSize)
	assert.Equal(t, 5, limits.MaxFiles)
	assert.Equal(t, int64(11*1024*1024), limits.MaxFileSize)
}

// TestZeroOrNegativeConfigFallsBackToTheDefault: an accidentally empty config value
// must not silently disable a limit.
func TestZeroOrNegativeConfigFallsBackToTheDefault(t *testing.T) {
	for _, bad := range []any{0, -1} {
		withUploadConfig(t, map[string]any{
			ConfigMaxMultipartSize: bad,
			ConfigMaxFiles:         bad,
			ConfigMaxFileSize:      bad,
		})

		limits := resolveUploadLimits()

		assert.Equal(t, DefaultMaxMultipartSize, limits.MaxMultipartSize)
		assert.Equal(t, DefaultMaxFiles, limits.MaxFiles)
		assert.Equal(t, DefaultMaxFileSize, limits.MaxFileSize)
	}
}

// TestConfiguredFileSizeIsEnforced drives the validator with the configured limit.
func TestConfiguredFileSizeIsEnforced(t *testing.T) {
	withUploadConfig(t, map[string]any{ConfigMaxFileSize: 11 * 1024 * 1024})

	parser := &MultipartParser{}
	limits := resolveUploadLimits()

	elevenMB := map[string][]*multipart.FileHeader{
		"avatar": {{Filename: "a.png", Size: 11 * 1024 * 1024}},
	}
	require.NoError(t, parser.validateFileSizes(elevenMB, limits),
		"an 11 MB file must pass once the limit is raised to 11 MB")

	twelveMB := map[string][]*multipart.FileHeader{
		"avatar": {{Filename: "a.png", Size: 12 * 1024 * 1024}},
	}
	err := parser.validateFileSizes(twelveMB, limits)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "File size exceeds maximum allowed size")
}

// TestDefaultFileSizeStillRejectsElevenMB pins the pre-v2 behaviour under the
// default config, so the fix is opt-in rather than a silent loosening.
func TestDefaultFileSizeStillRejectsElevenMB(t *testing.T) {
	withUploadConfig(t, nil)

	parser := &MultipartParser{}
	limits := resolveUploadLimits()

	err := parser.validateFileSizes(map[string][]*multipart.FileHeader{
		"avatar": {{Filename: "a.png", Size: 11 * 1024 * 1024}},
	}, limits)

	require.Error(t, err)
}

func TestConfiguredTotalFormSizeIsEnforced(t *testing.T) {
	withUploadConfig(t, map[string]any{ConfigMaxMultipartSize: 1024})

	parser := &MultipartParser{}
	limits := resolveUploadLimits()

	form := &multipart.Form{File: map[string][]*multipart.FileHeader{
		"a": {{Size: 600}},
		"b": {{Size: 600}},
	}}

	err := parser.validateFormSize(form, limits)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Total form size exceeds maximum allowed size of 1024 bytes")
}

// TestFormSizeSumIsNotTruncatedTo32Bit: the total was accumulated in an `int` and
// each file size cast with int(file.Size). On a 32-bit build that overflows, so a
// large enough upload could wrap to a small positive total and pass the check.
func TestFormSizeSumUsesInt64(t *testing.T) {
	withUploadConfig(t, map[string]any{ConfigMaxMultipartSize: 4 * 1024 * 1024 * 1024})

	parser := &MultipartParser{}
	limits := resolveUploadLimits()

	form := &multipart.Form{File: map[string][]*multipart.FileHeader{
		"big": {{Size: 3 * 1024 * 1024 * 1024}},
	}}

	require.NoError(t, parser.validateFormSize(form, limits))

	form.File["bigger"] = []*multipart.FileHeader{{Size: 2 * 1024 * 1024 * 1024}}
	require.Error(t, parser.validateFormSize(form, limits),
		"a 5 GiB total must exceed a 4 GiB limit rather than wrapping")
}
