package core

// Collection defines the interface for generic collections
type Collection[T any] interface {
	// Add adds a single element to the collection
	Add(el T) Collection[T]
	// AddAll adds all elements from another collection
	AddAll(col Collection[T]) Collection[T]
	// AddSlice adds all elements from a slice
	AddSlice(s []T) Collection[T]
	// Remove removes elements that match the closure condition
	Remove(closure func(el T) bool)
	// RemoveIndex removes an element at the specified index
	RemoveIndex(index int)
	// Size returns the number of elements in the collection
	Size() uint64
	// Get retrieves an element at the specified index
	Get(i int) T
	// Find returns the first element that matches the closure condition
	Find(closure func(el T) bool) T
	// FindAll returns a collection of elements that match the closure condition
	FindAll(closure func(el T) bool) Collection[T]
	// Unique returns a collection with duplicate elements removed based on the closure
	Unique(closure func(el T) bool) Collection[T]
	// Sort sorts the collection based on the closure condition
	Sort(closure func(el T) bool) Collection[T]
	// Each executes the closure for each element in the collection
	Each(closure func(el T))
	// ToSlice converts the collection to a slice
	ToSlice() []T
}

// Set defines the interface for generic sets
type Set[T any] interface {
	// Add adds a single element to the set
	Add(el T) Collection[T]
	// AddAll adds all elements from another collection
	AddAll(col Collection[T]) Collection[T]
	// AddSlice adds all elements from a slice
	AddSlice(s []T) Collection[T]
	// Remove removes an element from the set
	Remove(T any)
	// RemoveIndex removes an element at the specified index
	RemoveIndex(index int)
	// Size returns the number of elements in the set
	Size() uint64
	// Get retrieves an element at the specified index
	Get(i int) T
	// Each executes the closure for each element in the set
	Each(closure func(el T))
	// Find returns the first element that matches the closure condition
	Find(closure func(el T) bool) T
	// FindAll returns a set of elements that match the closure condition
	FindAll(closure func(el T) bool) Set[T]
}

// Map defines the interface for generic maps
type Map[T any] interface {
	// Set adds or updates a value for the given key
	Set(key string, value T) Map[T]
	// Get retrieves the value for the given key
	Get(key string) T
	// Remove removes the value for the given key
	Remove(key string)
	// Merge combines this map with another map
	Merge(m Map[T]) Map[T]
}
