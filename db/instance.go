package db

import (
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

var wrappers map[string]IDbWrapper

func SetDbInstance(name string, db IDbWrapper) {
	if wrappers == nil {
		wrappers = make(map[string]IDbWrapper)
	}
	wrappers[name] = db
}

func GetWrapper(name string) IDbWrapper {
	return wrappers[name]
}

type IDbWrapper interface {
	GetInstance() any
}

type GormWrapper struct {
	Gorm *gorm.DB
}

func (thiz GormWrapper) GetInstance() any {
	return thiz.Gorm
}

type PostgresWrapper struct {
	Pg *pgx.Conn
}

func (thiz PostgresWrapper) GetInstance() any {
	return thiz.Pg
}
