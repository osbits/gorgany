package fixtureservice

import (
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	dbcore "github.com/osbits/gorgany/v2/db/sql/core"

	fixturedomain "github.com/osbits/gorgany/v2/e2e/fixture-app/pkg/domain"
)

type UserService struct {
	DBContext core.IDBContext `container:"inject"`
}

func (s *UserService) Get(id any) (core.Authenticable, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return orm.New[*fixturedomain.User](session).Find(fmt.Sprint(id))
}

func (s *UserService) GetByUsername(username string) (core.Authenticable, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return orm.New[*fixturedomain.User](session).FirstByQuery(
		session.Query().Eq("username", username),
	)
}

func (s *UserService) Save(authEntity core.Authenticable) error {
	user, ok := authEntity.(*fixturedomain.User)
	if !ok {
		return fmt.Errorf("unexpected user type %T", authEntity)
	}

	session, err := s.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	return orm.New[*fixturedomain.User](session).Save(user)
}

func (s *UserService) newSession() (dbcore.ISession, error) {
	dataSource := s.DBContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return nil, fmt.Errorf("default data source is not configured")
	}

	return dataSource.NewSession()
}
