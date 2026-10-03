package migrations

import "testing"

func TestQueryWrap(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain placeholders", "SELECT a FROM t WHERE b = ? AND c = ?", "SELECT a FROM t WHERE b = $1 AND c = $2"},
		{"placeholder inside single quotes", "SELECT '?' FROM t WHERE b = ?", "SELECT '?' FROM t WHERE b = $1"},
		{"backtick inside single quotes", "SELECT '`' FROM t WHERE b = ?", "SELECT '`' FROM t WHERE b = $1"},
		{"single quote inside backticks", "SELECT `it's` FROM t WHERE b = ?", "SELECT `it's` FROM t WHERE b = $1"},
		{"placeholder inside double quotes", "SELECT \"a?b\" FROM t WHERE b = ?", "SELECT \"a?b\" FROM t WHERE b = $1"},
		{"doubled single quote", "SELECT 'it''s ?' FROM t WHERE b = ?", "SELECT 'it''s ?' FROM t WHERE b = $1"},
	}

	pg := &datastore{driverName: driverPostgres}
	other := &datastore{driverName: "sqlite3"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pg.QueryWrap(tt.in); got != tt.want {
				t.Errorf("postgres QueryWrap(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := other.QueryWrap(tt.in); got != tt.in {
				t.Errorf("non-postgres QueryWrap(%q) = %q, want unchanged", tt.in, got)
			}
		})
	}
}
