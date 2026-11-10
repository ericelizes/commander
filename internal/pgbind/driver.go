// Package pgbind adapts database/sql's portable question-mark parameters to
// PostgreSQL parameters. SQL semantics remain explicit in callers/migrations.
package pgbind

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const Driver = "commander-postgres"

func init() { sql.Register(Driver, &postgresDriver{}) }

type postgresDriver struct{}

func (*postgresDriver) Open(dsn string) (driver.Conn, error) {
	c, err := stdlib.GetDefaultDriver().Open(dsn)
	if err != nil {
		return nil, err
	}
	return &conn{Conn: c}, nil
}

// NewConnector preserves caller cancellation during connection establishment,
// including the PostgreSQL startup handshake, while retaining parameter binding.
func NewConnector(dsn string) (driver.Connector, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	return &connector{Connector: stdlib.GetConnector(*config)}, nil
}

type connector struct{ driver.Connector }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &conn{Conn: raw}, nil
}
func (c *connector) Driver() driver.Driver { return &postgresDriver{} }

type conn struct{ driver.Conn }

func (c *conn) Prepare(q string) (driver.Stmt, error) { return c.Conn.Prepare(Bind(q)) }
func (c *conn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, Bind(q))
}
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *conn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }
func (c *conn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}
func (c *conn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(v)
	}
	return driver.ErrSkip
}
func (c *conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, Bind(q), args)
}
func (c *conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, Bind(q), args)
}

// Bind preserves quoted strings/identifiers, dollar strings and SQL comments.
// JSON question-mark operators must be written as jsonb_exists() in portable SQL.
func Bind(q string) string {
	var out strings.Builder
	n := 0
	for i := 0; i < len(q); {
		start := i
		switch {
		case q[i] == '\'' || q[i] == '"':
			quote := q[i]
			i++
			for i < len(q) {
				if q[i] == quote {
					i++
					if i < len(q) && q[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case strings.HasPrefix(q[i:], "--"):
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case strings.HasPrefix(q[i:], "/*"):
			i += 2
			depth := 1
			for i < len(q) && depth > 0 {
				if strings.HasPrefix(q[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(q[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case q[i] == '$':
			j := i + 1
			for j < len(q) && ((q[j] >= 'a' && q[j] <= 'z') || (q[j] >= 'A' && q[j] <= 'Z') || q[j] == '_' || (j > i+1 && q[j] >= '0' && q[j] <= '9')) {
				j++
			}
			if j < len(q) && q[j] == '$' {
				delim := q[i : j+1]
				if end := strings.Index(q[j+1:], delim); end >= 0 {
					i = j + 1 + end + len(delim)
				} else {
					i = len(q)
				}
			} else {
				i++
			}
		case q[i] == '?':
			n++
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(n))
			i++
			continue
		default:
			i++
		}
		out.WriteString(q[start:i])
	}
	return out.String()
}
