package fixtureservice

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	dbcore "github.com/osbits/gorgany/v2/db/sql/core"

	fixturedomain "github.com/osbits/gorgany/v2/e2e/fixture-app/pkg/domain"
)

type WidgetService struct {
	DBContext core.IDBContext `container:"inject"`
}

func (s *WidgetService) List() ([]*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return orm.New[*fixturedomain.Widget](session).AllByQuery(
		session.Query().OrderBy("name", "ASC"),
	)
}

func (s *WidgetService) Get(id string) (*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	repo := orm.New[*fixturedomain.Widget](session)
	widget, err := repo.Find(id)
	if err != nil || widget == nil {
		return widget, err
	}
	if err := repo.LoadRelation(widget, "Tags"); err != nil {
		return nil, err
	}
	return widget, nil
}

func (s *WidgetService) Create(name, description, createdBy string) (*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	widget := &fixturedomain.Widget{
		ID:          uuid.NewString(),
		Name:        name,
		Description: description,
		CreatedBy:   createdBy,
	}
	if err := orm.New[*fixturedomain.Widget](session).Create(widget); err != nil {
		return nil, err
	}
	return widget, nil
}

func (s *WidgetService) Update(id, name, description string) (*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	repo := orm.New[*fixturedomain.Widget](session)
	widget, err := repo.Find(id)
	if err != nil {
		return nil, err
	}
	if widget == nil {
		return nil, nil
	}

	widget.Name = name
	widget.Description = description
	if err := repo.Save(widget); err != nil {
		return nil, err
	}
	return widget, nil
}

func (s *WidgetService) UpdateTags(id string, tagIDs []string) (*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	repo := orm.New[*fixturedomain.Widget](session)
	widget, err := repo.Find(id)
	if err != nil {
		return nil, err
	}
	if widget == nil {
		return nil, nil
	}

	tags, err := s.findTags(session, tagIDs)
	if err != nil {
		return nil, err
	}
	widget.Tags = tags
	if err := repo.Save(widget); err != nil {
		return nil, err
	}
	if err := repo.LoadRelation(widget, "Tags"); err != nil {
		return nil, err
	}
	return widget, nil
}

func (s *WidgetService) UpdateTagsDetached(id string, tagIDs []string) (*fixturedomain.Widget, error) {
	session, err := s.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	repo := orm.New[*fixturedomain.Widget](session)
	widget, err := repo.Find(id)
	if err != nil {
		return nil, err
	}
	if widget == nil {
		return nil, nil
	}

	tags := make([]*fixturedomain.Tag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		tags = append(tags, &fixturedomain.Tag{ID: tagID})
	}

	widget.Tags = tags
	if err := repo.Save(widget); err != nil {
		return nil, err
	}
	if err := repo.LoadRelation(widget, "Tags"); err != nil {
		return nil, err
	}
	return widget, nil
}

func (s *WidgetService) Count() (int64, error) {
	session, err := s.newSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()

	return orm.New[*fixturedomain.Widget](session).Count()
}

func (s *WidgetService) newSession() (dbcore.ISession, error) {
	dataSource := s.DBContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return nil, fmt.Errorf("default data source is not configured")
	}

	return dataSource.NewSession()
}

func (s *WidgetService) findTags(session dbcore.ISession, tagIDs []string) ([]*fixturedomain.Tag, error) {
	if len(tagIDs) == 0 {
		return []*fixturedomain.Tag{}, nil
	}

	repo := orm.New[*fixturedomain.Tag](session)
	tags := make([]*fixturedomain.Tag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		tag, err := repo.Find(tagID)
		if err != nil {
			return nil, err
		}
		if tag == nil {
			return nil, fmt.Errorf("tag %s not found", tagID)
		}
		tags = append(tags, tag)
	}

	return tags, nil
}
