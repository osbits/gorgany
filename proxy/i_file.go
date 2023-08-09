package proxy

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
