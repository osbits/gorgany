package util

func MergeMaps[T any](m1 map[string]T, m2 map[string]T) map[string]T {
	for k, v := range m2 {
		m1[k] = v
	}
	return m1
}

func ContainsKey[K comparable, V comparable](m map[K]V, key K) bool {
	if _, ok := m[key]; ok {
		return true
	}
	return false
}
