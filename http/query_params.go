package http

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
