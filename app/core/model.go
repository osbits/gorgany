package core

import "io"

type IDomain[T any] interface {
	Query() IOrm[T]
	Clone() *T
	GetDomainMeta() IDomainMeta
}

type IDomainMeta interface {
	SetLoaded(loaded bool)
	SetTable(table string)
	SetDriver(driver DbType)
	SetOriginal(original any)
	SetDomain(domain any)

	GetLoaded() bool
	GetTable() string
	GetDriver() DbType
	GetOriginal() any
	GetDomain() any
}

type IFile interface {
	SetName(name string)
	SetContent(content []byte) error

	GetName() string
	GetSize() int64
	GetContent() (io.ReadCloser, error)
	GetPath() string

	IsRead() bool
	IsSaved() bool

	FullPath() string
	PublicPath() string

	Save(path string) error
	IsExists() bool
	Delete() error
}

type ILocalizedString interface {
	Text(lang string) string
	Map() map[string]string
}

type LimitedFieldsMarshaller interface {
	AllowedProtectedFields() []string
	AllowedFields() []string
}

type ProtectedFields interface {
	GetProtectedFields() []string
}

type ISortParam interface {
	GetField() string
	GetOrder() string
}

type PaginationParams interface {
	GetPage() int
	GetLimit() int
	GetSort() []ISortParam
}

type LimitedFieldsParams interface {
	GetFields() []string
}

type NullableValueGetter interface {
	GetValue() any
}

type NullableValueSetter interface {
	SetValue(v any)
}

type IFormValue interface {
	Value() (any, error)
}
