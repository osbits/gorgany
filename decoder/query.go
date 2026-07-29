package decoder

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
)

func ParseUrlValues(params url.Values) (QueryParams, error) {
	processedParams := QueryParams{}

	regExp := regexp.MustCompile("(.+)\\[(\\d)\\]\\[(.+)\\]")

	elementsCount := make(map[string]int)
	for key := range params {
		if !regExp.MatchString(key) {
			continue
		}

		parsed := regExp.FindStringSubmatch(key)
		if len(parsed) != 4 {
			continue
		}

		elementName := parsed[1]
		index, err := strconv.Atoi(parsed[2])
		if err != nil {
			return nil, err
		}
		if elementsCount[elementName] < index+1 {
			elementsCount[elementName] = index + 1
		}
	}

	for key, param := range params {
		var value any
		if len(param) == 1 {
			value = param[0]
		} else {
			value = param
		}

		if !regExp.MatchString(key) {
			if str, ok := value.(string); ok && str == "" {
				continue
			}
			processedParams[key] = value
			continue
		}

		parsed := regExp.FindStringSubmatch(key)
		if len(parsed) != 4 {
			continue
		}

		elementName := parsed[1]
		index, err := strconv.Atoi(parsed[2])
		if err != nil {
			return nil, err
		}

		key := parsed[3]
		if processedParams[elementName] == nil {
			processedParams[elementName] = make([]map[string]string, elementsCount[elementName])
		}

		// These three assertions were unchecked and are reached straight from the
		// query string, so a request could pick the shape that panicked. The
		// `// todo: Test it` on the value assertion had been there since it was
		// written.
		elements, ok := processedParams[elementName].([]map[string]string)
		if !ok {
			return nil, fmt.Errorf(
				"query: parameter %q was already decoded as %T, so it cannot also be an indexed collection",
				elementName, processedParams[elementName])
		}
		if elements[index] == nil {
			elements[index] = make(map[string]string)
		}

		stringValue, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf(
				"query: parameter %q[%d].%s must be a string, got %T",
				elementName, index, key, value)
		}
		if stringValue != "" {
			elements[index][key] = stringValue
		}
	}

	return processedParams, nil
}

type QueryParams map[string]any

func (thiz QueryParams) GetString(key string) string {
	if val, ok := thiz[key].(string); ok {
		return val
	}
	return ""
}

func (thiz QueryParams) GetArray(key string) []string {
	if val, ok := thiz[key].([]string); ok {
		return val
	}
	return []string{}
}

func (thiz QueryParams) GetArrayMap(key string) []map[string]string {
	if val, ok := thiz[key].([]map[string]string); ok {
		return val
	}
	return []map[string]string{}
}

func (thiz QueryParams) AsMap() map[string]any {
	return thiz
}
