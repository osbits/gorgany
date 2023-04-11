package time

import "time"

type DateTimeDbWrap struct {
	Time time.Time
}

func (thiz DateTimeDbWrap) ToQueryBuilderValue() interface{} {
	return thiz.Time.Format("2006-01-02 15:01")
}

type DateDbWrap struct {
	Time time.Time
}

func (thiz DateDbWrap) ToQueryBuilderValue() interface{} {
	return thiz.Time.Format("2006-01-02")
}
