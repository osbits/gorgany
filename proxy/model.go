package proxy

type IDomain[T any] interface {
	Orm() IOrm[T]
}

type IFile interface {
	FullPath() string
	PublicPath() string
	PathInPublic() string
	ReadContent() (string, error)
	Save(p string) error
	IsExists() bool
	IsNil() bool
	Delete() error
}

type ILocalizedString interface {
	Text(lang string) string
	Map() map[string]string
}
