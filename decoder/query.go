package decoder

import (
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

		if processedParams[elementName].([]map[string]string)[index] == nil {
			processedParams[elementName].([]map[string]string)[index] = make(map[string]string)
		}

		if value.(string) != "" { // todo: Test it
			processedParams[elementName].([]map[string]string)[index][key] = value.(string)
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
