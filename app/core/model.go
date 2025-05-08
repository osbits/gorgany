package core

import "io"

// IDomain defines the interface for domain entities
type IDomain[T any] interface {
	// Query returns a new ORM query builder for this domain type
	Query() IOrm[T]
	// Clone creates a deep copy of the domain entity
	Clone() *T
	// GetDomainMeta returns the metadata for this domain entity
	GetDomainMeta() IDomainMeta
}

// IDomainMeta defines the interface for domain entity metadata
type IDomainMeta interface {
	// SetLoaded sets whether the entity has been loaded from storage
	SetLoaded(loaded bool)
	// SetTable sets the database table name for this entity
	SetTable(table string)
	// SetDriver sets the database driver type for this entity
	SetDriver(driver DbType)
	// SetOriginal sets the original data for this entity
	SetOriginal(original any)
	// SetDomain sets the domain entity instance
	SetDomain(domain any)

	// GetLoaded returns whether the entity has been loaded from storage
	GetLoaded() bool
	// GetTable returns the database table name for this entity
	GetTable() string
	// GetDriver returns the database driver type for this entity
	GetDriver() DbType
	// GetOriginal returns the original data for this entity
	GetOriginal() any
	// GetDomain returns the domain entity instance
	GetDomain() any
}

// IFile defines the interface for file operations
type IFile interface {
	// SetName sets the name of the file
	SetName(name string)

	// Read reads the file contents into the writer
	Read(writer io.Writer) (int64, error)
	// Write writes the contents from the reader to the specified path
	Write(path string, reader io.Reader) (int64, error)
	// Writer returns a writer for the file
	Writer() (io.WriteCloser, error)

	// GetName returns the name of the file
	GetName() string
	// GetPath returns the path of the file
	GetPath() string
	// GetSize returns the size of the file
	GetSize() (int64, error)

	// FullPath returns the complete path to the file
	FullPath() string
	// PublicPath returns the public URL path for the file
	PublicPath() string

	// IsExists checks if the file exists
	IsExists() bool
	// Delete removes the file
	Delete() error

	// Close closes the file
	Close() error
}

// ILocalizedString defines the interface for localized string values
type ILocalizedString interface {
	// Text returns the string value for the specified language
	Text(lang string) string
	// Map returns all language-value pairs as a map
	Map() map[string]string
}

// LimitedFieldsMarshaller defines the interface for controlling field marshaling
type LimitedFieldsMarshaller interface {
	// AllowedProtectedFields returns the list of protected fields that can be marshaled
	AllowedProtectedFields() []string
	// AllowedFields returns the list of fields that can be marshaled
	AllowedFields() []string
}

// ProtectedFields defines the interface for entities with protected fields
type ProtectedFields interface {
	// GetProtectedFields returns the list of protected fields
	GetProtectedFields() []string
}

// ISortParam defines the interface for sorting parameters
type ISortParam interface {
	// GetField returns the field to sort by
	GetField() string
	// GetOrder returns the sort order (asc/desc)
	GetOrder() string
}

// PaginationParams defines the interface for pagination parameters
type PaginationParams interface {
	// GetPage returns the current page number
	GetPage() int
	// GetLimit returns the number of items per page
	GetLimit() int
	// GetSort returns the sorting parameters
	GetSort() []ISortParam
}

// LimitedFieldsParams defines the interface for field limiting parameters
type LimitedFieldsParams interface {
	// GetFields returns the list of fields to include
	GetFields() []string
}

// NullableValueGetter defines the interface for getting nullable values
type NullableValueGetter interface {
	// GetValue returns the current value
	GetValue() any
}

// NullableValueSetter defines the interface for setting nullable values
type NullableValueSetter interface {
	// SetValue sets the value
	SetValue(v any)
}

// IFormValue defines the interface for form field values
type IFormValue interface {
	// Value returns the form field value
	Value() (any, error)
}

// Enum defines the interface for enumeration types
type Enum interface {
	// Values returns the string representation of the enum values
	Values() string
}

// IDomainContext defines the interface for domain context management
type IDomainContext interface {
	// RegisterDomain registers a new domain type
	RegisterDomain(pkgAndName string, domain any)
	// GetDomains returns all registered domain types
	GetDomains() map[string]any
}
