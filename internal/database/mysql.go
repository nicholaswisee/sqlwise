package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"sqlwise/internal/database/dialect"
)

var mysqlCapabilities = Capabilities{ConnectionTest: true, SchemaInspection: true, DataPreview: true, MigrationExecution: true}

func mysqlDB(c Connection) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = c.Username, c.Password, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database
	cfg.Timeout = ConnectTimeout
	switch c.TLS {
	case TLSRequire:
		cfg.TLSConfig = "skip-verify"
	case TLSVerifyFull:
		cfg.TLSConfig = "true"
	default:
		cfg.TLSConfig = "false"
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fail(ErrValidation, "Connection settings are invalid.")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

type mysqlPool struct {
	db       *sql.DB
	database string
}

func (p mysqlPool) handle() Handle { return &mysqlHandle{db: p.db, database: p.database} }

func (p mysqlPool) close() { p.db.Close() }

// mysqlHandle is limited to the registered database: in MySQL a schema is a
// database, and other databases visible to the login were not granted.
type mysqlHandle struct {
	db       *sql.DB
	database string
}

func (h *mysqlHandle) DBMS() DBMS { return MySQL }

func (h *mysqlHandle) Capabilities() Capabilities { return mysqlCapabilities }

func (h *mysqlHandle) Close() error { return nil }

func (h *mysqlHandle) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	return mysqlError(ctx, h.db.PingContext(ctx))
}

func (h *mysqlHandle) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	result, err := h.db.ExecContext(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (h *mysqlHandle) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return h.db.QueryRowContext(ctx, sql, args...)
}

func (h *mysqlHandle) Pin(ctx context.Context) (Conn, error) {
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return nil, mysqlError(ctx, err)
	}
	return mysqlConn{conn}, nil
}

type mysqlConn struct{ conn *sql.Conn }

func (c mysqlConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	result, err := c.conn.ExecContext(ctx, sql, args...)
	var server *mysql.MySQLError
	switch {
	case err == nil:
		return result.RowsAffected()
	case errors.As(err, &server):
		return 0, err
	}
	return 0, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
}

func (c mysqlConn) Close() error { return c.conn.Close() }

func mysqlError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var domain *Error
	if errors.As(err, &domain) {
		return err
	}
	if err := FromContext(ctx); err != nil {
		return err
	}
	var server *mysql.MySQLError
	if errors.As(err, &server) {
		switch server.Number {
		case 1044, 1045:
			return fail(ErrConnectionUnavailable, "The database rejected the username or password.")
		case 1049:
			return fail(ErrConnectionUnavailable, "The database does not exist on this server.")
		}
		return fail(ErrConnectionUnavailable, "The database rejected the request (MySQL error "+strconv.Itoa(int(server.Number))+").")
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return fail(ErrTimeout, "The database did not respond in time.")
	}
	return Unavailable()
}

func (h *mysqlHandle) Schemas(context.Context) ([]string, error) {
	return []string{h.database}, nil
}

func (h *mysqlHandle) InspectSchema(ctx context.Context, schema string) (SchemaSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	snapshot := SchemaSnapshot{DBMS: MySQL, Database: h.database, Schema: schema, CapturedAt: time.Now().UTC()}
	if schema != h.database {
		return snapshot, nil
	}
	if err := inspectMySQL(ctx, h.db, &snapshot); err != nil {
		return SchemaSnapshot{}, mysqlError(ctx, err)
	}
	return snapshot, nil
}

func inspectMySQL(ctx context.Context, db *sql.DB, snapshot *SchemaSnapshot) error {
	my := dialect.MySQL
	rows, err := db.QueryContext(ctx, `SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE = 'YES', c.COLUMN_DEFAULT
		FROM information_schema.COLUMNS c JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME AND t.TABLE_TYPE = 'BASE TABLE'
		WHERE c.TABLE_SCHEMA = ? ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`, snapshot.Schema)
	if err != nil {
		return err
	}
	index := make(map[string]int)
	for rows.Next() {
		var table string
		var column Column
		if err := rows.Scan(&table, &column.Name, &column.Type, &column.Nullable, &column.Default); err != nil {
			rows.Close()
			return err
		}
		i, ok := index[table]
		if !ok {
			i = len(snapshot.Tables)
			index[table] = i
			snapshot.Tables = append(snapshot.Tables, Table{Schema: snapshot.Schema, Name: table})
		}
		snapshot.Tables[i].Columns = append(snapshot.Tables[i].Columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = db.QueryContext(ctx, `SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE, k.COLUMN_NAME, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.KEY_COLUMN_USAGE k ON k.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND k.TABLE_NAME = tc.TABLE_NAME AND k.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.CONSTRAINT_TYPE IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY')
		ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME, k.ORDINAL_POSITION`, snapshot.Schema)
	if err != nil {
		return err
	}
	type keyed struct {
		table, kind, refTable string
		columns, refColumns   []string
	}
	var order []string
	keys := make(map[string]*keyed)
	backing := make(map[string]bool)
	for rows.Next() {
		var table, name, kind, column string
		var refTable, refColumn sql.NullString
		if err := rows.Scan(&table, &name, &kind, &column, &refTable, &refColumn); err != nil {
			rows.Close()
			return err
		}
		id := table + "\x00" + name
		k, ok := keys[id]
		if !ok {
			k = &keyed{table: table, kind: kind, refTable: refTable.String}
			keys[id] = k
			order = append(order, id)
			backing[id] = true
		}
		k.columns = append(k.columns, column)
		if refColumn.Valid {
			k.refColumns = append(k.refColumns, refColumn.String)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	quoteAll := func(names []string) string {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = my.Quote(n)
		}
		return strings.Join(quoted, ", ")
	}
	for _, id := range order {
		k := keys[id]
		i, ok := index[k.table]
		if !ok {
			continue
		}
		_, name, _ := strings.Cut(id, "\x00")
		switch k.kind {
		case "PRIMARY KEY":
			snapshot.Tables[i].PrimaryKey = k.columns
		case "UNIQUE":
			snapshot.Tables[i].Constraints = append(snapshot.Tables[i].Constraints, Constraint{Name: name, Kind: Unique, Columns: k.columns, Definition: "UNIQUE (" + quoteAll(k.columns) + ")"})
		case "FOREIGN KEY":
			snapshot.Tables[i].Constraints = append(snapshot.Tables[i].Constraints, Constraint{Name: name, Kind: ForeignKey, Columns: k.columns,
				Definition: "FOREIGN KEY (" + quoteAll(k.columns) + ") REFERENCES " + my.Quote(k.refTable) + " (" + quoteAll(k.refColumns) + ")"})
		}
	}

	rows, err = db.QueryContext(ctx, `SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.CONSTRAINT_TYPE = 'CHECK' ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME`, snapshot.Schema)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, name, clause string
		if err := rows.Scan(&table, &name, &clause); err != nil {
			rows.Close()
			return err
		}
		if i, ok := index[table]; ok {
			snapshot.Tables[i].Constraints = append(snapshot.Tables[i].Constraints, Constraint{Name: name, Kind: Check, Definition: "CHECK (" + clause + ")"})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = db.QueryContext(ctx, `SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE = 0, COALESCE(COLUMN_NAME, CONCAT('(', EXPRESSION, ')'))
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, snapshot.Schema)
	if err != nil {
		return err
	}
	var indexOrder []string
	indexes := make(map[string]*Index)
	indexColumns := make(map[string][]string)
	for rows.Next() {
		var table, name, column string
		var unique bool
		if err := rows.Scan(&table, &name, &unique, &column); err != nil {
			rows.Close()
			return err
		}
		id := table + "\x00" + name
		if backing[id] || name == "PRIMARY" {
			continue
		}
		if _, ok := indexes[id]; !ok {
			indexes[id] = &Index{Name: name, Unique: unique}
			indexOrder = append(indexOrder, id)
		}
		if strings.HasPrefix(column, "(") {
			indexColumns[id] = append(indexColumns[id], column)
		} else {
			indexColumns[id] = append(indexColumns[id], my.Quote(column))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range indexOrder {
		table, _, _ := strings.Cut(id, "\x00")
		i, ok := index[table]
		if !ok {
			continue
		}
		idx := indexes[id]
		kind := "INDEX "
		if idx.Unique {
			kind = "UNIQUE INDEX "
		}
		idx.Definition = kind + my.Quote(idx.Name) + " ON " + my.Quote(table) + " (" + strings.Join(indexColumns[id], ", ") + ")"
		snapshot.Tables[i].Indexes = append(snapshot.Tables[i].Indexes, *idx)
	}
	return nil
}

var mysqlBinaryTypes = map[string]bool{"binary": true, "varbinary": true, "tinyblob": true, "blob": true, "mediumblob": true, "longblob": true, "bit": true,
	"geometry": true, "point": true, "linestring": true, "polygon": true, "multipoint": true, "multilinestring": true, "multipolygon": true, "geomcollection": true}

func (h *mysqlHandle) ReadTable(ctx context.Context, req TableRequest) (TablePage, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	page, err := h.readTable(ctx, req)
	if err != nil {
		return TablePage{}, mysqlError(ctx, err)
	}
	return page, nil
}

func (h *mysqlHandle) readTable(ctx context.Context, req TableRequest) (TablePage, error) {
	page := TablePage{Table: req.Table, Offset: req.Offset, Limit: req.Limit}
	if req.Table.Schema != h.database {
		return page, fail(ErrNotFound, "Table not found.")
	}
	var found int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'`,
		req.Table.Schema, req.Table.Name).Scan(&found)
	if err != nil {
		return page, err
	}
	if found == 0 {
		return page, fail(ErrNotFound, "Table not found.")
	}
	rows, err := h.db.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, req.Table.Schema, req.Table.Name)
	if err != nil {
		return page, err
	}
	var binary []bool
	for rows.Next() {
		var column ResultColumn
		var dataType string
		if err := rows.Scan(&column.Name, &column.Type, &dataType); err != nil {
			rows.Close()
			return page, err
		}
		page.Columns = append(page.Columns, column)
		binary = append(binary, mysqlBinaryTypes[strings.ToLower(dataType)])
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	rows, err = h.db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' ORDER BY ORDINAL_POSITION`, req.Table.Schema, req.Table.Name)
	if err != nil {
		return page, err
	}
	var key []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			return page, err
		}
		key = append(key, dialect.MySQL.Quote(column))
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	my := dialect.MySQL
	exprs := make([]string, len(page.Columns))
	for i, c := range page.Columns {
		if binary[i] {
			exprs[i] = "SUBSTRING(" + my.Quote(c.Name) + ", 1, " + strconv.Itoa(MaxCellBytes/2) + ")"
		} else {
			exprs[i] = "LEFT(CAST(" + my.Quote(c.Name) + " AS CHAR), " + strconv.Itoa(MaxCellBytes+1) + ")"
		}
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM " + my.Qualify(req.Table.Schema, req.Table.Name)
	if len(key) > 0 {
		query += " ORDER BY " + strings.Join(key, ", ")
		page.Ordered = true
	}
	rows, err = h.db.QueryContext(ctx, query+" LIMIT ? OFFSET ?", req.Limit+1, req.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		if len(page.Rows) == req.Limit {
			page.HasNext = true
			break
		}
		values := scanTargets(binary)
		if err := rows.Scan(values...); err != nil {
			return page, err
		}
		if !addRow(&page, &size, cells(values)) {
			break
		}
	}
	return page, rows.Err()
}
