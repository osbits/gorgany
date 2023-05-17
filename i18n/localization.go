package i18n

import (
	"fmt"
	"regexp"
)

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
