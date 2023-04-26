package db

import (
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

var dbInstance map[string]IDbWrapper

func SetDbInstance(name string, db IDbWrapper) {
	if dbInstance == nil {
		dbInstance = make(map[string]IDbWrapper)
	}
	dbInstance[name] = db
}

func GetDbInstance(name string) IDbWrapper {
	return dbInstance[name]
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
