package pgbind

import "testing"

func TestBindPreservesSQLLiteralsAndComments(t *testing.T) {
	q := "SELECT ?, '?', \"?\", $$?$$, $body$?$body$, ? -- ?\n/* ? /* ? */ ? */ WHERE x=?"
	want := "SELECT $1, '?', \"?\", $$?$$, $body$?$body$, $2 -- ?\n/* ? /* ? */ ? */ WHERE x=$3"
	if got := Bind(q); got != want {
		t.Fatalf("got %s; want %s", got, want)
	}
}
