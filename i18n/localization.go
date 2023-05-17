package i18n

import (
	"fmt"
	"github.com/spf13/viper"
	"regexp"
)

func AvailableLocales() []string {
	enabled := viper.GetBool("i18n.enabled")
	availableLang := make([]string, 0)
	if !enabled {
		return availableLang
	}

	availableLang = append(availableLang, viper.GetString("i18n.lang.default"))
	availableLang = append(availableLang, viper.GetStringSlice("i18n.lang.available")...)

	return availableLang
}

func Translation(code string, opts map[string]any, locale string) string {
	config := GetManager().GetConfig(locale)
	message := config.GetString(code)

	regex := regexp.MustCompile(`\{\:(?P<key>.+?)\}`)

	processedMessage := regex.ReplaceAllStringFunc(message, func(pattern string) string {
		foundStrings := regex.FindStringSubmatch(pattern)
		if len(foundStrings) != 2 {
			return pattern
		}

		key := foundStrings[1]
		val, ok := opts[key]
		if !ok {
			return pattern
		}

		return fmt.Sprintf("%v", val)
	})

	return processedMessage
}

func TranslationWithSequence(code string, locale string, opts ...[]any) string {
	config := GetManager().GetConfig(locale)
	message := config.GetString(code)

	regex := regexp.MustCompile(`\{\:(?P<key>.+?)\}`)

	i := 0
	processedMessage := regex.ReplaceAllStringFunc(message, func(pattern string) string {
		foundStrings := regex.FindStringSubmatch(pattern)
		if len(foundStrings) != 2 {
			return pattern
		}

		if len(opts) > i {
			val := opts[i][0]
			i++
			return fmt.Sprintf("%v", val)
		} else {
			return pattern
		}
	})

	return processedMessage
}
