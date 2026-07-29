package core

import "sort"

// SortedKeys returns the keys of m in lexical order.
//
// Dialects use it wherever SQL is generated from a map — UPDATE ... SET,
// ON CONFLICT DO UPDATE SET — so the emitted clause order is stable. Ranging a
// Go map directly yields a different column order on every call, which does not
// corrupt results (arguments are appended in the same pass, so SQL and args stay
// aligned) but does make the SQL untestable and defeats any statement cache
// keyed on the query text.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
