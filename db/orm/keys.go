package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm/schema"
)

// keySchema parses the schema that names entity's primary key columns.
//
// Every statement that addresses one existing row — Delete, Update, the existence probe
// behind UpdateExisting, and Refresh — reads its key from here, so they cannot disagree
// about which columns identify a row. A schema gorm could not parse is refused rather than
// guessed at: the fallback these paths used to share assumed a single column called "id",
// and a WHERE that leaves out a key column matches every row that shares the rest.
func keySchema(entity any) (*schema.Schema, error) {
	entitySchema, err := schema.Parse(entity, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return nil, fmt.Errorf("orm: cannot read the primary key of %T: %w", entity, err)
	}
	return entitySchema, nil
}

// pkPredicate returns the conditions that address exactly the row v holds: one equality per
// primary key column, by column name.
//
// v is the entity's struct value, not a pointer to it. The column is always the field's
// DBName. Delete used to put the Go field name in its WHERE (`UserID = ?` for a column
// called user_id), and every path used only the first key column, so on a composite key
// Update wrote, and Delete removed, every row that shared the first part. A zero part is
// refused for the same reason a zero single key always was: it addresses a row that was
// never written, or all of them.
//
// A pointer key column is judged, and bound, by the value it points at. gorm's ValueOf
// calls only a nil pointer zero, so &0 and &"" used to pass and reach the server as a key of
// 0 or an empty string, and error messages printed the pointer's address instead of the key.
func pkPredicate(s *schema.Schema, v reflect.Value) ([]dbCore.Condition, error) {
	if s == nil || len(s.PrimaryFields) == 0 {
		table := "this entity"
		if s != nil {
			table = s.Table
		}
		return nil, fmt.Errorf("orm: %s has no primary key column, so no single row can be addressed", table)
	}

	v = reflect.Indirect(v)
	conditions := make([]dbCore.Condition, 0, len(s.PrimaryFields))
	for _, field := range s.PrimaryFields {
		value, zero := field.ValueOf(context.Background(), v)
		if pointer := reflect.ValueOf(value); !zero && pointer.Kind() == reflect.Ptr {
			if pointer.IsNil() {
				zero = true
			} else {
				value = pointer.Elem().Interface()
				zero = pointer.Elem().IsZero()
			}
		}
		if zero {
			return nil, fmt.Errorf("orm: primary key column %q of %s is zero", field.DBName, s.Table)
		}
		conditions = append(conditions, &dbCore.BinaryCondition{
			Left:     field.DBName,
			Operator: "=",
			Right:    value,
		})
	}
	return conditions, nil
}

// refuseKeyChange refuses an UPDATE whose DirtyColumns names a primary key column.
//
// The ORM does not remember the key a row was loaded with, so an UPDATE's WHERE is built
// from the key as the entity holds it now. A caller that changed a key column and marked it
// dirty meant to move the row to a new key. What the statement did instead was address
// whichever row already held the new key and overwrite its other columns with this entity's,
// leave the row the entity was loaded from untouched, and report success. The key column
// cannot go in the SET list either, since the WHERE needs it to find the row. So the change is
// refused before anything is sent, naming the first such column in key order.
func refuseKeyChange(s *schema.Schema, meta *EntityMeta) error {
	if meta == nil || len(meta.DirtyColumns) == 0 {
		return nil
	}
	for _, column := range s.PrimaryFieldDBNames {
		if meta.DirtyColumns[column] {
			return fmt.Errorf("orm: cannot change primary key column %q of %s through Update; "+
				"delete the row and create it again", column, s.Table)
		}
	}
	return nil
}

// describeKey renders a key predicate for an error message, as `a = 1 AND b = 2`.
//
// A message that names only the first column of a composite key names a set of rows, not
// the one the caller was writing, so every column is printed.
func describeKey(conditions []dbCore.Condition) string {
	parts := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		if binary, ok := condition.(*dbCore.BinaryCondition); ok {
			parts = append(parts, fmt.Sprintf("%v %s %v", binary.Left, binary.Operator, binary.Right))
			continue
		}
		sql, _ := condition.ToSQL()
		parts = append(parts, sql)
	}
	return strings.Join(parts, " AND ")
}

// whereAll ANDs every condition into builder's WHERE.
func whereAll(builder dbCore.IQueryBuilder, conditions []dbCore.Condition) dbCore.IQueryBuilder {
	for _, condition := range conditions {
		builder = builder.Where(condition)
	}
	return builder
}
