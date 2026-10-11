package db

import "testing"

func TestAlterTableSqlBuilder_ToSQL(t *testing.T) {
	tests := []struct {
		name    string
		builder *AlterTableSqlBuilder
		want    string
		wantErr bool
	}{
		{
			name: "MySQL add int",
			builder: DialectMySQL.
				AlterTable("the_table").
				AddColumn(DialectMySQL.Column("the_col", ColumnTypeInteger, UnsetSize)),
			want:    "ALTER TABLE the_table ADD COLUMN the_col INT NOT NULL",
			wantErr: false,
		},
		{
			name: "MySQL add string",
			builder: DialectMySQL.
				AlterTable("the_table").
				AddColumn(DialectMySQL.Column("the_col", ColumnTypeVarChar, OptionalInt{true, 128})),
			want:    "ALTER TABLE the_table ADD COLUMN the_col VARCHAR(128) NOT NULL",
			wantErr: false,
		},

		{
			name: "MySQL add int and string",
			builder: DialectMySQL.
				AlterTable("the_table").
				AddColumn(DialectMySQL.Column("first_col", ColumnTypeInteger, UnsetSize)).
				AddColumn(DialectMySQL.Column("second_col", ColumnTypeVarChar, OptionalInt{true, 128})),
			want:    "ALTER TABLE the_table ADD COLUMN first_col INT NOT NULL, ADD COLUMN second_col VARCHAR(128) NOT NULL",
			wantErr: false,
		},
		{
			name: "MySQL change column",
			builder: DialectMySQL.
				AlterTable("oauth_users").
				ChangeColumn("remote_user_id", DialectMySQL.Column("remote_user_id", ColumnTypeVarChar, OptionalInt{true, 128})),
			want: "ALTER TABLE oauth_users CHANGE COLUMN remote_user_id remote_user_id VARCHAR(128) NOT NULL",
		},
		{
			name: "Postgres add varchar with default",
			builder: DialectPostgres.
				AlterTable("the_table").
				AddColumn(DialectPostgres.Column("the_col", ColumnTypeVarChar, OptionalInt{true, 24}).SetDefault("")),
			want: "ALTER TABLE the_table ADD COLUMN the_col VARCHAR(24) NOT NULL DEFAULT ''",
		},
		{
			name: "Postgres change column",
			builder: DialectPostgres.
				AlterTable("oauth_users").
				ChangeColumn("remote_user_id", DialectPostgres.Column("remote_user_id", ColumnTypeVarChar, OptionalInt{true, 128})),
			want: "ALTER TABLE oauth_users ALTER COLUMN remote_user_id TYPE VARCHAR(128) USING remote_user_id::VARCHAR(128), ALTER COLUMN remote_user_id SET NOT NULL, ALTER COLUMN remote_user_id DROP DEFAULT",
		},
		{
			name: "Postgres change nullable column with default",
			builder: DialectPostgres.
				AlterTable("t").
				ChangeColumn("c", DialectPostgres.Column("c", ColumnTypeInteger, UnsetSize).SetNullable(true).SetDefault("0")),
			want: "ALTER TABLE t ALTER COLUMN c TYPE INTEGER USING c::INTEGER, ALTER COLUMN c DROP NOT NULL, ALTER COLUMN c SET DEFAULT 0",
		},
		{
			name: "Postgres change column with rename is refused",
			builder: DialectPostgres.
				AlterTable("t").
				ChangeColumn("old", DialectPostgres.Column("new", ColumnTypeInteger, UnsetSize)),
			wantErr: true,
		},
		{
			name:    "Postgres rename column",
			builder: DialectPostgres.AlterTable("t").RenameColumn("old", "new"),
			want:    "ALTER TABLE t RENAME COLUMN old TO new",
		},
		{
			name: "Postgres rename combined with another change is refused",
			builder: DialectPostgres.
				AlterTable("t").
				RenameColumn("old", "new").
				AddColumn(DialectPostgres.Column("c", ColumnTypeInteger, UnsetSize)),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.builder.ToSQL()
			if (err != nil) != tt.wantErr {
				t.Errorf("ToSQL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ToSQL() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDropIndexSqlBuilder_ToSQL(t *testing.T) {
	for _, tt := range []struct {
		d    DialectType
		want string
	}{
		{DialectMySQL, "DROP INDEX idx on tbl"},
		{DialectSQLite, "DROP INDEX idx on tbl"},
		{DialectPostgres, "DROP INDEX idx"},
	} {
		got, err := tt.d.DropIndex("idx", "tbl").ToSQL()
		if err != nil || got != tt.want {
			t.Errorf("dialect %d: got %q, %v; want %q", tt.d, got, err, tt.want)
		}
	}
}
