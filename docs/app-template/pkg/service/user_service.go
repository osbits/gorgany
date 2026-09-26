package service

import (
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"

	"myapp/pkg/domain"
)

// UserService is the core.IUserService the auth strategies resolve users through.
type UserService struct {
	DBContext core.IDBContext `container:"inject"`
}

func (s *UserService) Get(id any) (core.Authenticable, error) {
	session, err := newSession(s.DBContext)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return orm.New[*domain.User](session).Find(fmt.Sprint(id))
}

func (s *UserService) GetByUsername(username string) (core.Authenticable, error) {
	session, err := newSession(s.DBContext)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return orm.New[*domain.User](session).FirstByQuery(session.Query().Eq("username", username))
}

func (s *UserService) Save(entity core.Authenticable) error {
	user, ok := entity.(*domain.User)
	if !ok {
		return fmt.Errorf("unexpected user type %T", entity)
	}
	session, err := newSession(s.DBContext)
	if err != nil {
		return err
	}
	defer session.Close()
	return orm.New[*domain.User](session).Save(user)
}
