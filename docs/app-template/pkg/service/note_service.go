package service

import (
	"github.com/google/uuid"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"

	"myapp/pkg/domain"
)

type NoteService struct {
	DBContext core.IDBContext `container:"inject"`
}

func (s *NoteService) Create(title string) (*domain.Note, error) {
	session, err := newSession(s.DBContext)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	note := &domain.Note{ID: uuid.NewString(), Title: title}
	if err := orm.New[*domain.Note](session).Create(note); err != nil {
		return nil, err
	}
	return note, nil
}

func (s *NoteService) Find(id string) (*domain.Note, error) {
	session, err := newSession(s.DBContext)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	note, err := orm.New[*domain.Note](session).Find(id)
	if err != nil || note == nil {
		return nil, &NotFoundError{Entity: "note", ID: id}
	}
	return note, nil
}

func (s *NoteService) All() ([]*domain.Note, error) {
	session, err := newSession(s.DBContext)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return orm.New[*domain.Note](session).All()
}
