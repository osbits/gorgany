package auth

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db/orm"
)

type ISessionRepository interface {
	FindById(id string) (*DbSessionEntity, error)
	Save(session *DbSessionEntity) error
	Delete(session *DbSessionEntity) error
	DeleteById(id string) error
	DeleteExpired() error
}

type DbSessionRepository struct {
	dbContext core.IDBContext `container:"inject"`
}

func NewDbSessionRepository() *DbSessionRepository {
	return &DbSessionRepository{}
}

func (r *DbSessionRepository) withOrm(operation func(*orm.ORM[*DbSessionEntity]) error) error {
	dataSource := r.dbContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return fmt.Errorf("no data source available")
	}
	
	dbSession, err := dataSource.NewSession()
	if err != nil {
		return err
	}
	defer dbSession.Close()
	
	orm := orm.New[*DbSessionEntity](dbSession)
	return operation(orm)
}

func (r *DbSessionRepository) FindById(id string) (*DbSessionEntity, error) {
	var session *DbSessionEntity
	err := r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
		var findErr error
		session, findErr = orm.Find(id)
		return findErr
	})
	return session, err
}

func (r *DbSessionRepository) Save(session *DbSessionEntity) error {
	return r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
		return orm.Save(session)
	})
}

func (r *DbSessionRepository) Delete(session *DbSessionEntity) error {
	return r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
		return orm.Delete(session)
	})
}

func (r *DbSessionRepository) DeleteById(id string) error {
	session, err := r.FindById(id)
	if err != nil {
		return err
	}
	return r.Delete(session)
}

func (r *DbSessionRepository) DeleteExpired() error {
	return r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
		query := "DELETE FROM sessions WHERE expiry < NOW()"
		_, err := orm.RawQuery(query)
		return err
	})
}
