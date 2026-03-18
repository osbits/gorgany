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
	msg := cfg.GetString(code)

	regex := regexp.MustCompile(`\{\:(?P<key>.+?)\}`)
	processed := regex.ReplaceAllStringFunc(msg, func(pattern string) string {
		parts := regex.FindStringSubmatch(pattern)
		if len(parts) != 2 {
			return pattern
		}
		key := parts[1]
		if val, ok := opts[key]; ok {
			return fmt.Sprintf("%v", val)
		}
		return pattern
	})
	return processed
}

func TranslationWithSequence(code string, locale string, opts ...any) string {
	cfg := GetManager().GetConfig(locale)
	msg := cfg.GetString(code)

	regex := regexp.MustCompile(`\{\:(?P<key>.+?)\}`)
	i := 0
	processed := regex.ReplaceAllStringFunc(msg, func(pattern string) string {
		parts := regex.FindStringSubmatch(pattern)
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
