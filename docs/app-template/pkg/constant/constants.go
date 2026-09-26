package constant

// ApiNamespace prefixes every JSON API route: a RouteConfig with Namespace "api"
// is served under /api.
const ApiNamespace = "api"

// V1 is the path prefix of version 1 of the API. Routes write constant.V1 + "/notes",
// never a literal "/v1", so a version's routes can be found and moved together.
const V1 = "/v1"
