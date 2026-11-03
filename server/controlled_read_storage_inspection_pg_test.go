package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5/stdlib"
)

// This fixture changes roles/schemas only in the parent-owned disposable
// container verified by the wrapper. It authenticates no real execution host.
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
		t.Fatal("owned container guard required")
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
	if len(out.Relations) != 16 || out.ResolutionBasis != "inspected_connection_search_path" || len(out.Unassessed) < 6 {
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
	if first.Resolved || first.OrdinaryTable != nil || len(first.Columns) != 0 || len(first.TablePrivileges) != 0 {
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
	if err != nil { // Some supported configurations surface permission errors.
		if !errors.Is(err, ErrControlledReadStorageInspection) || len(report.Relations) != 0 {
			t.Fatal("permission error became absence report")
		}
		return
	}
	r := storageInspectionRelation(t, report, "evidence_revision_initials")
	if !r.NameObservedInCatalog || r.Resolved || r.OrdinaryTable != nil || len(r.Columns) != 0 {
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
	enabled, readOnly, unexpected atomic.Bool
	selects, commits              atomic.Int64
}

func (t *storageInspectionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.enabled.Load() {
		return ctx
	}
	sqlText := strings.ToLower(strings.TrimSpace(data.SQL))
	switch {
	case strings.HasPrefix(sqlText, "begin"):
		t.readOnly.Store(strings.Contains(sqlText, "read only"))
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
	if !trace.readOnly.Load() || trace.selects.Load() == 0 || trace.commits.Load() != 0 || trace.unexpected.Load() {
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
			if mode == "internal-bound" && time.Since(start) > 6*time.Second {
				t.Fatal("internal inspection bound not honored")
			}
		})
	}
}

func TestControlledStorageInspectionInvalidConfiguration(t *testing.T) {
	for _, s := range []*Server{nil, {}, {m: &Manager{}}, {m: &Manager{pg: &pgdb.DB{}}}} {
		out, err := s.InspectControlledReadStorage(t.Context())
		if !errors.Is(err, ErrControlledReadStorageInspection) || len(out.Relations) != 0 {
			t.Fatal("invalid server accepted")
		}
	}
	if out, err := (&Server{}).InspectControlledReadStorage(nil); !errors.Is(err, ErrControlledReadStorageInspection) || len(out.Relations) != 0 {
		t.Fatal("nil context accepted")
	}
	f := newStorageInspectionFixture(t, false)
	p := f.pool(t, []string{f.schema}, "", nil)
	if p.Close() != nil {
		t.Fatal("fixture close")
	}
	if out, err := storageInspectionServer(p).InspectControlledReadStorage(t.Context()); !errors.Is(err, ErrControlledReadStorageInspection) || len(out.Relations) != 0 || strings.Contains(err.Error(), f.schema) {
		t.Fatal("closed database leaked diagnostics")
	}
}
