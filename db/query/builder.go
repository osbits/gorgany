package query

type Builder interface {
}

type BuilderValue interface {
	ToQueryBuilderValue() interface{}
}
