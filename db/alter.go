package db

import (
	"fmt"
	"strings"
)

type AlterTableSqlBuilder struct {
	Dialect DialectType
	Name    string
	Changes []string
	err     error
}

func (b *AlterTableSqlBuilder) AddColumn(col *Column) *AlterTableSqlBuilder {
	if colVal, err := col.String(); err == nil {
		b.Changes = append(b.Changes, fmt.Sprintf("ADD COLUMN %s", colVal))
	}
	return b
}

// ChangeColumn replaces column name's definition with col, renaming it if
// col.Name differs.
//
// Postgres has no CHANGE COLUMN, so there it becomes ALTER COLUMN … TYPE plus
// the nullability and default changes col implies. Postgres cannot rename a
// column in the same statement as other changes, so a rename on Postgres is
// an error from ToSQL; use RenameColumn in a builder of its own.
func (b *AlterTableSqlBuilder) ChangeColumn(name string, col *Column) *AlterTableSqlBuilder {
	if b.Dialect == DialectPostgres {
		return b.changeColumnPostgres(name, col)
	}
	if colVal, err := col.String(); err == nil {
		b.Changes = append(b.Changes, fmt.Sprintf("CHANGE COLUMN %s %s", name, colVal))
	}
	return b
}

func (b *AlterTableSqlBuilder) changeColumnPostgres(name string, col *Column) *AlterTableSqlBuilder {
	if col.Name != name {
		b.setErr(fmt.Errorf("table %s: Postgres cannot rename column %s to %s alongside other changes; use RenameColumn on its own", b.Name, name, col.Name))
		return b
	}
	if col.PrimaryKey {
		b.setErr(fmt.Errorf("table %s: ChangeColumn cannot add a primary key on Postgres", b.Name))
		return b
	}
	typeStr, err := col.Type.Format(DialectPostgres, col.Size)
	if err != nil {
		b.setErr(err)
		return b
	}
	b.Changes = append(b.Changes, fmt.Sprintf("ALTER COLUMN %s TYPE %s USING %s::%s", name, typeStr, name, typeStr))
	if col.Nullable {
		b.Changes = append(b.Changes, fmt.Sprintf("ALTER COLUMN %s DROP NOT NULL", name))
	} else {
		b.Changes = append(b.Changes, fmt.Sprintf("ALTER COLUMN %s SET NOT NULL", name))
	}
	if col.Default.Set {
		val := col.Default.Value
		if val == "" {
			val = "''"
		}
		b.Changes = append(b.Changes, fmt.Sprintf("ALTER COLUMN %s SET DEFAULT %s", name, val))
	} else {
		// MySQL's CHANGE COLUMN drops a default the new definition omits.
		b.Changes = append(b.Changes, fmt.Sprintf("ALTER COLUMN %s DROP DEFAULT", name))
	}
	return b
}

// RenameColumn renames column from to to. On Postgres it must be the only
// change in its builder.
func (b *AlterTableSqlBuilder) RenameColumn(from, to string) *AlterTableSqlBuilder {
	b.Changes = append(b.Changes, fmt.Sprintf("RENAME COLUMN %s TO %s", from, to))
	return b
}

func (b *AlterTableSqlBuilder) setErr(err error) {
	if b.err == nil {
		b.err = err
	}
}

func (b *AlterTableSqlBuilder) AddUniqueConstraint(name string, columns ...string) *AlterTableSqlBuilder {
	b.Changes = append(b.Changes, fmt.Sprintf("ADD CONSTRAINT %s UNIQUE (%s)", name, strings.Join(columns, ", ")))
	return b
}

func (b *AlterTableSqlBuilder) ToSQL() (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.Dialect == DialectPostgres && len(b.Changes) > 1 {
		for _, c := range b.Changes {
			if strings.HasPrefix(c, "RENAME ") {
				return "", fmt.Errorf("table %s: Postgres cannot combine RENAME COLUMN with other changes", b.Name)
			}
		}
	}

	var str strings.Builder

	str.WriteString("ALTER TABLE ")
	str.WriteString(b.Name)
	str.WriteString(" ")

	if len(b.Changes) == 0 {
		return "", fmt.Errorf("no changes provide for table: %s", b.Name)
	}
	changeCount := len(b.Changes)
	for i, thing := range b.Changes {
		str.WriteString(thing)
		if i < changeCount-1 {
			str.WriteString(", ")
		}
	}

	return str.String(), nil
}
