package db

import "fmt"

type DialectType int

const (
	DialectSQLite   DialectType = iota
	DialectMySQL    DialectType = iota
	DialectPostgres DialectType = iota
)

// valid reports whether d is a dialect this package can write SQL for.
func (d DialectType) valid() bool {
	switch d {
	case DialectSQLite, DialectMySQL, DialectPostgres:
		return true
	}
	return false
}

func (d DialectType) Column(name string, t ColumnType, size OptionalInt) *Column {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &Column{Dialect: d, Name: name, Type: t, Size: size}
}

func (d DialectType) Table(name string) *CreateTableSqlBuilder {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &CreateTableSqlBuilder{Dialect: d, Name: name}
}

func (d DialectType) AlterTable(name string) *AlterTableSqlBuilder {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &AlterTableSqlBuilder{Dialect: d, Name: name}
}

func (d DialectType) CreateUniqueIndex(name, table string, columns ...string) *CreateIndexSqlBuilder {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &CreateIndexSqlBuilder{Dialect: d, Name: name, Table: table, Unique: true, Columns: columns}
}

func (d DialectType) CreateIndex(name, table string, columns ...string) *CreateIndexSqlBuilder {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &CreateIndexSqlBuilder{Dialect: d, Name: name, Table: table, Unique: false, Columns: columns}
}

func (d DialectType) DropIndex(name, table string) *DropIndexSqlBuilder {
	if !d.valid() {
		panic(fmt.Sprintf("unexpected dialect: %d", d))
	}
	return &DropIndexSqlBuilder{Dialect: d, Name: name, Table: table}
}
