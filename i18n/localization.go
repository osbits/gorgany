package i18n

import (
	"fmt"
	"regexp"

	"github.com/spf13/viper"
)

func AllLocales() []string {
	locs := []string{viper.GetString("i18n.lang.default")}
	locs = append(locs, viper.GetStringSlice("i18n.lang.available")...)
	return locs
}

func AvailableLocales() []string {
	if !viper.GetBool("i18n.enabled") {
		return []string{}
	}
	return AllLocales()
}

func Translation(code string, opts map[string]any, locale string) string {
	cfg := GetManager().GetConfig(locale)
	return Interpolate(cfg.GetString(code), opts)
}

// placeholderPattern matches the `{:key}` placeholders a translation carries.
var placeholderPattern = regexp.MustCompile(`\{\:(?P<key>.+?)\}`)

// Interpolate substitutes `{:key}` placeholders in msg from opts, leaving any
// placeholder it has no value for visible rather than blanking it.
//
// This is exported because the validator's built-in message catalog (B2) needs the same
// placeholder syntax for messages that never came from a translation file. Sharing the
// implementation is what keeps a `{:param}` in an app's own translation and a
// `{:param}` in a framework default from diverging.
func Interpolate(msg string, opts map[string]any) string {
	if msg == "" || len(opts) == 0 {
		return msg
	}

	return placeholderPattern.ReplaceAllStringFunc(msg, func(pattern string) string {
		parts := placeholderPattern.FindStringSubmatch(pattern)
		if len(parts) != 2 {
			return pattern
		}
		if val, ok := opts[parts[1]]; ok {
			return fmt.Sprintf("%v", val)
		}
		return pattern
	})
}

func TranslationWithSequence(code string, locale string, opts ...any) string {
	cfg := GetManager().GetConfig(locale)
	msg := cfg.GetString(code)

	i := 0
	processed := placeholderPattern.ReplaceAllStringFunc(msg, func(pattern string) string {
		parts := placeholderPattern.FindStringSubmatch(pattern)
		if len(parts) != 2 {
			return pattern
		}
		if i < len(opts) {
			val := opts[i]
			i++
			return fmt.Sprintf("%v", val)
		}
		return pattern
	})
	return processed
}

func DefaultLocale() string {
	return viper.GetString("i18n.lang.default")
}
