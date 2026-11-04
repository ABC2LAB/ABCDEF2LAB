package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// This fixture executes CREATE/DROP SCHEMA and ROLE. Its marker checks format
// only, not ownership: supply an independently verified, owned disposable DB.
// There is no repository wrapper that verifies container CID/name/labels.
type storageInspectionFixture struct {
	admin          *sql.DB
	owner          *pgdb.DB
	dsn, schema    string
	pools          []*sql.DB
	schemas, roles []string
}

func newStorageInspectionFixture(t *testing.T, full bool) *storageInspectionFixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_STORAGE_INSPECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("owned storage inspection PostgreSQL not supplied")
	}
	if !regexp.MustCompile(`^codex-owned-pg-v1:[a-f0-9]{64}$`).MatchString(os.Getenv("ARTEX_STORAGE_INSPECTION_OWNED_PG")) {
		t.Fatal("owned disposable database marker format required; marker does not prove ownership")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("fixture connection configuration")
	}
	admin := stdlib.OpenDB(*config)
	f := &storageInspectionFixture{admin: admin, dsn: dsn}
	t.Cleanup(func() {
		for _, p := range f.pools {
			if p.Close() != nil {
				t.Error("fixture pool close")
			}
		}
		if f.owner != nil && f.owner.Close() != nil {
			t.Error("fixture owner close")
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, s := range f.schemas {
			if !strings.HasPrefix(s, "cri_schema_") {
				t.Error("unexpected cleanup schema")
				continue
			}
			if _, e := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{s}.Sanitize()+" CASCADE"); e != nil {
				t.Error("owned schema cleanup")
			}
		}
		for _, r := range f.roles {
			if !strings.HasPrefix(r, "cri_role_") {
				t.Error("unexpected cleanup role")
				continue
			}
			if _, e := admin.ExecContext(cleanupCtx, "DROP ROLE "+pgx.Identifier{r}.Sanitize()); e != nil {
				t.Error("owned role cleanup")
			}
		}
		if admin.Close() != nil {
			t.Error("fixture admin close")
		}
	})
	f.schema = f.addSchema(t)
	if full {
		// Run the actual current migrations, not an inventory-derived mock schema.
		u, err := url.Parse(dsn)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			t.Fatal("fixture URL form required")
		}
		q := u.Query()
		q.Set("search_path", pgx.Identifier{f.schema}.Sanitize())
		u.RawQuery = q.Encode()
		f.owner, err = pgdb.Open(u.String())
		if err != nil {
			t.Fatal("actual ARTEX fixture migration")
		}
		if auditcheckpoint.Migrate(t.Context(), f.owner.DB) != nil || evidencerevision.Migrate(t.Context(), f.owner.DB) != nil {
			t.Fatal("actual explicit core migrations")
		}
	} else {
		f.owner = &pgdb.DB{DB: f.pool(t, []string{f.schema}, "", nil)}
		// owner is already in pools and is closed there exactly once.
		f.pools = f.pools[:len(f.pools)-1]
	}
	return f
}

func (f *storageInspectionFixture) addSchema(t *testing.T) string {
	t.Helper()
	name := "cri_schema_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.admin.ExecContext(t.Context(), "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal("create owned schema")
	}
	f.schemas = append(f.schemas, name)
	return name
}

func (f *storageInspectionFixture) addRole(t *testing.T) string {
	t.Helper()
	name := "cri_role_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.admin.ExecContext(t.Context(), "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" NOLOGIN"); err != nil {
		t.Fatal("create owned role")
	}
	f.roles = append(f.roles, name)
	return name
}

func (f *storageInspectionFixture) pool(t *testing.T, schemas []string, role string, trace *storageInspectionTrace) *sql.DB {
	t.Helper()
	cfg, err := pgx.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal("fixture config")
	}
	var quoted []string
	for _, s := range schemas {
		quoted = append(quoted, pgx.Identifier{s}.Sanitize())
	}
	cfg.RuntimeParams["search_path"] = strings.Join(quoted, ",")
	if trace != nil {
		cfg.Tracer = trace
	}
	p := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, c *pgx.Conn) error {
		if role != "" {
			_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
		return nil
	}))
	p.SetMaxOpenConns(1)
	p.SetMaxIdleConns(1)
	f.pools = append(f.pools, p)
	if p.PingContext(t.Context()) != nil {
		t.Fatal("fixture connection")
	}
	return p
}

func storageInspectionServer(p *sql.DB) *Server { return &Server{m: &Manager{pg: &pgdb.DB{DB: p}}} }
func storageInspectionRun(t *testing.T, p *sql.DB) ControlledReadStorageReport {
	t.Helper()
	out, err := storageInspectionServer(p).InspectControlledReadStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func storageInspectionRelation(t *testing.T, r ControlledReadStorageReport, name string) ControlledReadStorageRelation {
	t.Helper()
	for _, f := range r.Relations {
		if f.Name == name {
			return f
		}
	}
	t.Fatal("fixed relation absent from report")
	return ControlledReadStorageRelation{}
}
func storageInspectionColumn(t *testing.T, r ControlledReadStorageRelation, name string) ControlledReadStorageColumn {
	t.Helper()
	for _, c := range r.Columns {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("fixed column absent from report")
	return ControlledReadStorageColumn{}
}
func storageInspectionPrivilege(t *testing.T, r ControlledReadStorageRelation, name string) bool {
	t.Helper()
	for _, p := range r.TablePrivileges {
		if p.Privilege == name {
			return p.Granted
		}
	}
	t.Fatal("privilege not assessed")
	return false
}

func TestControlledStorageInspectionActualMigrations(t *testing.T) {
	f := newStorageInspectionFixture(t, true)
	out := storageInspectionRun(t, f.owner.DB)
	if len(out.Relations) != 17 || out.ResolutionBasis != "inspected_connection_search_path" || len(out.Unassessed) < 6 {
		t.Fatal("report boundaries")
	}
	for _, r := range out.Relations {
		if !r.NameObservedInCatalog || !r.Resolved || r.OrdinaryTable == nil || !*r.OrdinaryTable {
			t.Fatalf("actual migration relation mismatch: %s", r.Name)
		}
		for _, c := range r.Columns {
			if !c.Present || !c.BuiltinTypeMatches {
				t.Fatalf("actual migration column mismatch: %s.%s", r.Name, c.Name)
			}
		}
		for _, p := range r.TablePrivileges {
			if !p.Granted {
				t.Fatal("fixture owner privilege")
			}
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal("serialize public facts")
	}
	for _, forbidden := range []string{f.schema, "ready", "current_user", "password", "secret_identity", "anchor_digest_value"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("report contains private data or authorization conclusion")
		}
	}
}

func TestControlledStorageInspectionUnresolvedAndPartial(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	first := storageInspectionRelation(t, storageInspectionRun(t, f.owner.DB), "evidence_revision_initials")
	if first.NameObservedInCatalog || first.Resolved || first.OrdinaryTable != nil || len(first.Columns) != 0 || len(first.TablePrivileges) != 0 {
		t.Fatal("unresolved relation was assessed as present/absent columns")
	}
	if _, err := f.owner.ExecContext(t.Context(), `CREATE TABLE evidence_revision_initials(revision TEXT,task_id BIGINT)`); err != nil {
		t.Fatal("partial fixture")
	}
	partial := storageInspectionRelation(t, storageInspectionRun(t, f.owner.DB), "evidence_revision_initials")
	if !partial.Resolved || !partial.NameObservedInCatalog {
		t.Fatal("partial relation unresolved")
	}
	if c := storageInspectionColumn(t, partial, "revision"); !c.Present || c.BuiltinTypeMatches {
		t.Fatal("wrong type accepted")
	}
	if c := storageInspectionColumn(t, partial, "task_id"); !c.Present || !c.BuiltinTypeMatches {
		t.Fatal("correct column mismatch")
	}
	if c := storageInspectionColumn(t, partial, "intent_id"); c.Present || c.BuiltinTypeMatches {
		t.Fatal("missing column accepted")
	}
}

func TestControlledStorageInspectionSearchPathAndSchemaUsage(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	if _, err := f.owner.ExecContext(t.Context(), `CREATE TABLE evidence_revision_initials(revision BIGINT)`); err != nil {
		t.Fatal("owner relation")
	}
	shadow := f.addSchema(t)
	if _, err := f.admin.ExecContext(t.Context(), "CREATE TABLE "+pgx.Identifier{shadow, "evidence_revision_initials"}.Sanitize()+"(revision TEXT)"); err != nil {
		t.Fatal("shadow relation")
	}
	p := f.pool(t, []string{shadow, f.schema}, "", nil)
	wrong := storageInspectionRelation(t, storageInspectionRun(t, p), "evidence_revision_initials")
	if storageInspectionColumn(t, wrong, "revision").BuiltinTypeMatches {
		t.Fatal("ignored actual search_path precedence")
	}
	role := f.addRole(t)
	denied := f.pool(t, []string{f.schema}, role, nil)
	report, err := storageInspectionServer(denied).InspectControlledReadStorage(t.Context())
	// In PG16 an inaccessible schema is skipped during unqualified resolution:
	// to_regclass returns NULL, rather than a permission-error fallback.
	if err != nil {
		t.Fatal("PG16 schema USAGE absence must produce unresolved facts")
	}
	r := storageInspectionRelation(t, report, "evidence_revision_initials")
	if !r.NameObservedInCatalog || r.Resolved || r.OrdinaryTable != nil || len(r.Columns) != 0 || len(r.TablePrivileges) != 0 {
		t.Fatal("schema USAGE absence became object absence")
	}
}

func TestControlledStorageInspectionIndividualRolePrivileges(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	if _, err := f.owner.ExecContext(t.Context(), `CREATE TABLE evidence_revision_initials(revision BIGINT)`); err != nil {
		t.Fatal("role table")
	}
	role := f.addRole(t)
	quotedRole := pgx.Identifier{role}.Sanitize()
	table := pgx.Identifier{f.schema, "evidence_revision_initials"}.Sanitize()
	if _, err := f.admin.ExecContext(t.Context(), "GRANT USAGE ON SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+quotedRole); err != nil {
		t.Fatal("owned schema usage grant")
	}
	if _, err := f.admin.ExecContext(t.Context(), "GRANT SELECT ON "+table+" TO "+quotedRole); err != nil {
		t.Fatal("owned SELECT grant")
	}
	p := f.pool(t, []string{f.schema}, role, nil)
	first := storageInspectionRelation(t, storageInspectionRun(t, p), "evidence_revision_initials")
	if !storageInspectionPrivilege(t, first, "SELECT") || storageInspectionPrivilege(t, first, "INSERT") || storageInspectionPrivilege(t, first, "UPDATE") {
		t.Fatal("privileges combined with ANY semantics")
	}
	if _, err := f.admin.ExecContext(t.Context(), "GRANT INSERT,UPDATE(revision) ON "+table+" TO "+quotedRole); err != nil {
		t.Fatal("owned per privilege grant")
	}
	second := storageInspectionRelation(t, storageInspectionRun(t, p), "evidence_revision_initials")
	if !storageInspectionPrivilege(t, second, "INSERT") || storageInspectionPrivilege(t, second, "UPDATE") {
		t.Fatal("column UPDATE mistaken for table UPDATE")
	}
	// Table UPDATE=false cannot prove FOR UPDATE impossible: a column grant
	// suffices here. The table is empty, so no fixture payload is read.
	rows, err := p.QueryContext(t.Context(), "SELECT revision FROM "+table+" FOR UPDATE")
	if err != nil {
		t.Fatal("actual column-authorized query rejected")
	}
	if rows.Close() != nil {
		t.Fatal("role query close")
	}
}

func TestControlledStorageInspectionRejectsOperatorShadowing(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	if _, err := f.owner.ExecContext(t.Context(), `CREATE TABLE evidence_revision_initials(revision BIGINT)`); err != nil {
		t.Fatal("operator fixture relation")
	}
	function := pgx.Identifier{f.schema, "inspection_hostile_equal"}.Sanitize()
	if _, err := f.admin.ExecContext(t.Context(), "CREATE FUNCTION "+function+"(pg_catalog.text,pg_catalog.text) RETURNS pg_catalog.bool LANGUAGE plpgsql VOLATILE AS $$ BEGIN RAISE EXCEPTION 'synthetic hostile operator'; END $$"); err != nil {
		t.Fatal("owned hostile function")
	}
	if _, err := f.admin.ExecContext(t.Context(), "CREATE OPERATOR "+pgx.Identifier{f.schema}.Sanitize()+".= (LEFTARG=pg_catalog.text,RIGHTARG=pg_catalog.text,FUNCTION="+function+")"); err != nil {
		t.Fatal("owned hostile operator")
	}
	p := f.pool(t, []string{f.schema, "pg_catalog"}, "", nil)
	var probe bool
	if err := p.QueryRowContext(t.Context(), `SELECT 'probe'::pg_catalog.text = 'probe'::pg_catalog.text`).Scan(&probe); err == nil {
		t.Fatal("hostile operator was not selected in control")
	}
	out := storageInspectionRun(t, p)
	r := storageInspectionRelation(t, out, "evidence_revision_initials")
	if !r.Resolved || !storageInspectionColumn(t, r, "revision").BuiltinTypeMatches {
		t.Fatal("qualified catalog query invoked hostile operator or changed relation resolution")
	}
}

type storageInspectionTrace struct {
	enabled, readOnly, repeatableRead, unexpected atomic.Bool
	selects, commits                              atomic.Int64
}

func (t *storageInspectionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.enabled.Load() {
		return ctx
	}
	sqlText := strings.ToLower(strings.TrimSpace(data.SQL))
	switch {
	case strings.HasPrefix(sqlText, "begin"):
		t.readOnly.Store(strings.Contains(sqlText, "read only"))
		t.repeatableRead.Store(strings.Contains(sqlText, "repeatable read"))
	case strings.HasPrefix(sqlText, "select"), strings.HasPrefix(sqlText, "with"):
		t.selects.Add(1)
	case strings.HasPrefix(sqlText, "rollback"):
	case strings.HasPrefix(sqlText, "commit"):
		t.commits.Add(1)
	default:
		t.unexpected.Store(true)
	}
	return ctx
}
func (*storageInspectionTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestControlledStorageInspectionNoPayloadWriteOrSequence(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	if _, err := f.owner.ExecContext(t.Context(), `CREATE SEQUENCE inspection_probe; CREATE VIEW evidence_revision_initials AS SELECT nextval('inspection_probe')::BIGINT AS revision`); err != nil {
		t.Fatal("side-effect view fixture")
	}
	var before int64
	var beforeCalled bool
	if f.owner.QueryRowContext(t.Context(), `SELECT last_value,is_called FROM inspection_probe`).Scan(&before, &beforeCalled) != nil {
		t.Fatal("sequence snapshot")
	}
	trace := &storageInspectionTrace{}
	p := f.pool(t, []string{f.schema}, "", trace)
	trace.enabled.Store(true)
	out := storageInspectionRun(t, p)
	trace.enabled.Store(false)
	r := storageInspectionRelation(t, out, "evidence_revision_initials")
	if !r.Resolved || r.OrdinaryTable == nil || *r.OrdinaryTable {
		t.Fatal("view kind misreported")
	}
	var after int64
	var afterCalled bool
	if f.owner.QueryRowContext(t.Context(), `SELECT last_value,is_called FROM inspection_probe`).Scan(&after, &afterCalled) != nil || before != after || beforeCalled != afterCalled {
		t.Fatal("inspection evaluated payload/allocated sequence")
	}
	if !trace.readOnly.Load() || !trace.repeatableRead.Load() || trace.selects.Load() == 0 || trace.commits.Load() != 0 || trace.unexpected.Load() {
		t.Fatal("actual driver transaction was not read-only rollback")
	}
	var objects int
	if f.admin.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1`, f.schema).Scan(&objects) != nil || objects != 2 {
		t.Fatal("inspection migrated or created objects")
	}
}

func TestControlledStorageInspectionCancellationAndBound(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	for _, mode := range []string{"cancelled", "caller-deadline", "internal-bound"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			cancel := func() {}
			if mode == "cancelled" {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "caller-deadline" {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
			}
			defer cancel()
			var held *sql.Conn
			if mode != "cancelled" {
				f.owner.DB.SetMaxOpenConns(1)
				var err error
				held, err = f.owner.DB.Conn(t.Context())
				if err != nil {
					t.Fatal("hold own connection")
				}
				defer held.Close()
			}
			start := time.Now()
			out, err := storageInspectionServer(f.owner.DB).InspectControlledReadStorage(ctx)
			if !errors.Is(err, ErrControlledReadStorageInspection) || len(out.Relations) != 0 || out.ResolutionBasis != "" {
				t.Fatal("cancel/timeout returned facts")
			}
			want := "controlled_read_storage_inspection_unavailable:deadline"
			if mode == "cancelled" {
				want = "controlled_read_storage_inspection_unavailable:cancelled"
			}
			if err.Error() != want {
				t.Fatalf("reason = %q, want %q", err.Error(), want)
			}
			if mode == "internal-bound" && time.Since(start) > 6*time.Second {
				t.Fatal("internal inspection bound not honored")
			}
		})
	}
}

func TestControlledStorageInspectionInvalidConfiguration(t *testing.T) {
	for _, s := range []*Server{nil, {}, {m: &Manager{}}, {m: &Manager{pg: &pgdb.DB{}}}} {
		out, err := s.InspectControlledReadStorage(t.Context())
		if !errors.Is(err, ErrControlledReadStorageInspection) || err.Error() != "controlled_read_storage_inspection_unavailable:configuration" || len(out.Relations) != 0 {
			t.Fatal("invalid server accepted")
		}
	}
	if out, err := (&Server{}).InspectControlledReadStorage(nil); !errors.Is(err, ErrControlledReadStorageInspection) || err.Error() != "controlled_read_storage_inspection_unavailable:configuration" || len(out.Relations) != 0 {
		t.Fatal("nil context accepted")
	}
}

func TestControlledStorageInspectionClosedDatabase(t *testing.T) {
	f := newStorageInspectionFixture(t, false)
	p := f.pool(t, []string{f.schema}, "", nil)
	if p.Close() != nil {
		t.Fatal("fixture close")
	}
	if out, err := storageInspectionServer(p).InspectControlledReadStorage(t.Context()); !errors.Is(err, ErrControlledReadStorageInspection) || len(out.Relations) != 0 || strings.Contains(err.Error(), f.schema) {
		t.Fatal("closed database leaked diagnostics")
	}
}

func TestControlledStorageInspectionFixedErrors(t *testing.T) {
	capture := &durableStageLog{}
	previous := log.Writer()
	log.SetOutput(capture)
	t.Cleanup(func() { log.SetOutput(previous) })
	private := errors.New("private-driver-detail-must-not-escape")
	for _, state := range []string{"42501", "bad\nprivate-driver-detail-must-not-escape", "lower", ""} {
		cause := errors.Join(&pgconn.PgError{Code: state, Message: private.Error()}, private)
		err := storageInspectionDriverError(t.Context(), "catalog", cause)
		if err.Error() != "controlled_read_storage_inspection_unavailable:catalog" || !errors.Is(err, ErrControlledReadStorageInspection) || errors.Is(err, private) {
			t.Fatal("driver cause escaped fixed error boundary")
		}
	}
	for _, cause := range []error{private, nil} {
		_ = storageInspectionDriverError(t.Context(), "transaction", cause)
	}
	text := capture.String()
	if !strings.Contains(text, "stage=catalog sqlstate=42501") || strings.Count(text, "sqlstate=unavailable") != 5 || strings.Contains(text, private.Error()) || strings.Contains(text, "lower") {
		t.Fatal("SQLSTATE-only diagnostic leaked driver data or lost fixed diagnostic")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		ctx    context.Context
		reason string
	}{{cancelled, "cancelled"}, {expired, "deadline"}} {
		if err := storageInspectionError(tc.ctx, "transaction"); err.Error() != "controlled_read_storage_inspection_unavailable:"+tc.reason {
			t.Fatal("caller cancellation reason lost")
		}
	}
}

func TestControlledStorageInspectionDirectRevisionSQLInventory(t *testing.T) {
	source, err := os.ReadFile("../evidencerevision/store.go")
	if err != nil {
		t.Fatal("read current direct revision SQL")
	}
	inventory := map[string]map[string]string{}
	for _, object := range storageInspectionObjects() {
		inventory[object.name] = map[string]string{}
		for _, entry := range strings.Fields(object.columns) {
			pair := strings.SplitN(entry, ":", 2)
			inventory[object.name][pair[0]] = pair[1]
		}
	}
	// Compare actual fixed SELECT/WHERE columns, rather than building a schema
	// from the inventory itself. Broader helper dependencies remain unassessed.
	matches := regexp.MustCompile(`SELECT ([a-z_,]+) FROM (tasks|task_archives) WHERE ([a-z_]+)=`).FindAllStringSubmatch(string(source), -1)
	seen := map[string]bool{}
	for _, match := range matches {
		seen[match[2]] = true
		for _, column := range append(strings.Split(match[1], ","), match[3]) {
			if inventory[match[2]][column] == "" {
				t.Fatalf("direct revision SQL missing from inventory: %s.%s", match[2], column)
			}
		}
	}
	if !seen["tasks"] || !seen["task_archives"] || inventory["tasks"]["deleted_at"] != "timestamptz" || inventory["task_archives"]["state"] != "text" {
		t.Fatal("direct archive/deletion SQL or required builtin type not covered")
	}
}

// A local database/sql driver fixes the intermediate-failure boundary without
// network/DB access. The successful control returns every unresolved fact.
type storageInspectionConnector struct{ conn *storageInspectionConn }

func (c storageInspectionConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c storageInspectionConnector) Driver() driver.Driver                        { return storageInspectionDriver{} }

type storageInspectionDriver struct{}

func (storageInspectionDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type storageInspectionConn struct {
	options          driver.TxOptions
	remaining        time.Duration
	calls, rollbacks int
	failOn           int
}

func (*storageInspectionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*storageInspectionConn) Close() error { return nil }
func (*storageInspectionConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected legacy begin")
}
func (c *storageInspectionConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options = options
	deadline, ok := ctx.Deadline()
	if ok {
		c.remaining = time.Until(deadline)
	}
	return storageInspectionTx{c}, nil
}
func (c *storageInspectionConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.calls++
	if c.failOn != 0 && c.calls == c.failOn {
		return nil, &pgconn.PgError{Code: "42501", Message: "private query detail"}
	}
	return &storageInspectionRows{}, nil
}

type storageInspectionTx struct{ c *storageInspectionConn }

func (storageInspectionTx) Commit() error      { return errors.New("unexpected commit") }
func (tx storageInspectionTx) Rollback() error { tx.c.rollbacks++; return nil }

type storageInspectionRows struct{ done bool }

func (*storageInspectionRows) Columns() []string {
	return []string{"observed", "resolved", "oid", "ordinary", "select", "insert", "update"}
}
func (*storageInspectionRows) Close() error { return nil }
func (r *storageInspectionRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(values, []driver.Value{false, false, nil, nil, nil, nil, nil})
	return nil
}

func TestControlledStorageInspectionTransactionAndPartialFailure(t *testing.T) {
	for _, failOn := range []int{0, 2} {
		t.Run(map[int]string{0: "complete", 2: "after-first-relation"}[failOn], func(t *testing.T) {
			conn := &storageInspectionConn{failOn: failOn}
			pool := sql.OpenDB(storageInspectionConnector{conn})
			defer pool.Close()
			out, err := storageInspectionServer(pool).InspectControlledReadStorage(t.Context())
			if conn.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) || !conn.options.ReadOnly || conn.remaining <= 0 || conn.remaining > controlledStorageInspectionTimeout || conn.rollbacks != 1 {
				t.Fatal("transaction lost isolation/read-only/deadline/rollback boundary")
			}
			if failOn == 0 {
				if err != nil || len(out.Relations) != 17 || conn.calls != 17 {
					t.Fatal("valid synthetic control rejected")
				}
				for _, relation := range out.Relations {
					if relation.NameObservedInCatalog || relation.Resolved {
						t.Fatal("absent catalog entry became observed")
					}
				}
			} else if err == nil || err.Error() != "controlled_read_storage_inspection_unavailable:catalog" || conn.calls != 2 || len(out.Relations) != 0 || out.ResolutionBasis != "" || len(out.Unassessed) != 0 {
				t.Fatal("intermediate catalog failure exposed a partial report or raw error")
			}
		})
	}
}
