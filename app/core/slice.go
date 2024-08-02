package core

type Collection[T any] interface {
	Add(el T) Collection[T]
	AddAll(col Collection[T]) Collection[T]
	AddSlice(s []T) Collection[T]
	Remove(closure func(el T) bool)
	RemoveIndex(index int)
	Size() uint64
	Get(i int) T
	Find(closure func(el T) bool) T
	FindAll(closure func(el T) bool) Collection[T]
	Unique(closure func(el T) bool) Collection[T]
	Sort(closure func(el T) bool) Collection[T]
	Each(closure func(el T))
	ToSlice() []T
}

type Set[T any] interface {
	Add(el T) Collection[T]
	AddAll(col Collection[T]) Collection[T]
	AddSlice(s []T) Collection[T]
	Remove(T any)
	RemoveIndex(index int)
	Size() uint64
	Get(i int) T
	Each(closure func(el T))
	Find(closure func(el T) bool) T
	FindAll(closure func(el T) bool) Set[T]
}

type Map[T any] interface {
	Set(key string, value T) Map[T]
	Get(key string) T
	Remove(key string)
	Merge(m Map[T]) Map[T]
}
