package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// legacyObsRow holds the columns that exist in the pre-conflict-surfacing schema.
// Only the columns that existed in the legacy DDL are captured here.
type legacyObsRow struct {
	syncID    string
	sessionID string
	obsType   string
	title     string
	content   string
	project   string
	scope     string
}

// newTestStoreWithLegacySchema creates a temporary SQLite database using the
// pre-memory-conflict-surfacing DDL (v_N), inserts the given fixture rows via
// raw SQL, then calls New(cfg) so that migrate() runs against the legacy
// database.  Returns a *Store ready for assertion.
//
// The raw SQLite DB is closed before New() is called so that the WAL file
// is fully flushed and the connection can be re-opened by New().
func newTestStoreWithLegacySchema(t *testing.T, fixtureRows []legacyObsRow) *Store {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// 1. Open raw DB and apply legacy DDL.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: open raw db: %v", err)
	}

	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: WAL pragma: %v", err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: foreign_keys pragma: %v", err)
	}

	if _, err := raw.Exec(legacyDDLPreMemoryConflictSurfacing); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: apply legacy DDL: %v", err)
	}

	// 2. Insert fixture rows.  We need a session row first because of the FK.
	if len(fixtureRows) > 0 {
		if _, err := raw.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-migration-test', 'engram', '/tmp')`,
		); err != nil {
			raw.Close()
			t.Fatalf("newTestStoreWithLegacySchema: insert session: %v", err)
		}
		for _, row := range fixtureRows {
			if _, err := raw.Exec(
				`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope)
				 VALUES (?, 'ses-migration-test', ?, ?, ?, ?, ?)`,
				row.syncID, row.obsType, row.title, row.content, row.project, row.scope,
			); err != nil {
				raw.Close()
				t.Fatalf("newTestStoreWithLegacySchema: insert fixture row %+v: %v", row, err)
			}
		}
	}

	// 3. Close raw DB so the file is released before New() re-opens it.
	if err := raw.Close(); err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: close raw db: %v", err)
	}

	// 4. Open via New() — this calls migrate() against the legacy DB.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: New(cfg): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// fixtureRows returns a stable set of 5 legacy observation rows used across
// the migration tests.
func migrationFixtureRows() []legacyObsRow {
	return []legacyObsRow{
		{syncID: "obs-legacy-001", obsType: "decision", title: "Use sessions for auth", content: "We chose session-based auth", project: "engram", scope: "project"},
		{syncID: "obs-legacy-002", obsType: "bugfix", title: "Fixed tokenizer", content: "Normalized tokenizer panic on edge case", project: "engram", scope: "project"},
		{syncID: "obs-legacy-003", obsType: "architecture", title: "Hexagonal arch agreed", content: "We follow hexagonal architecture", project: "engram", scope: "project"},
		{syncID: "obs-legacy-004", obsType: "pattern", title: "Container-presentational pattern", content: "Use container components for state", project: "engram", scope: "project"},
		{syncID: "obs-legacy-005", obsType: "policy", title: "No secrets in commits", content: "Never commit .env or credentials", project: "engram", scope: "project"},
	}
}

// ─── A.3: TestMigrate_PreMemoryConflictSurfacing_PreservesData ─────────────────
//
// RED test: verifies that after migrate() runs on a legacy DB:
//   - all 5 fixture rows are intact (id, sync_id, content unchanged)
//   - new columns (review_after, expires_at) are NULL for pre-existing rows
//   - memory_relations table exists
//
// This test FAILS (red) until Phase B adds the new columns and table to migrate().

func TestMigrate_PreMemoryConflictSurfacing_PreservesData(t *testing.T) {
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)

	// Assert all 5 rows are still present with their original sync_ids and content.
	rows, err := s.db.Query(
		`SELECT sync_id, content FROM observations WHERE session_id = 'ses-migration-test' ORDER BY id`,
	)
	if err != nil {
		t.Fatalf("query fixture rows: %v", err)
	}
	defer rows.Close()

	var got []struct{ syncID, content string }
	for rows.Next() {
		var r struct{ syncID, content string }
		if err := rows.Scan(&r.syncID, &r.content); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(got) != len(fixtures) {
		t.Fatalf("expected %d rows after migration, got %d", len(fixtures), len(got))
	}
	for i, fix := range fixtures {
		if got[i].syncID != fix.syncID {
			t.Errorf("row %d: sync_id = %q, want %q", i, got[i].syncID, fix.syncID)
		}
		if got[i].content != fix.content {
			t.Errorf("row %d: content = %q, want %q", i, got[i].content, fix.content)
		}
	}

	// Assert new columns exist and are NULL for pre-existing rows.
	// This will fail (red) until Phase B adds the columns to migrate().
	nullRows, err := s.db.Query(
		`SELECT sync_id, review_after, expires_at FROM observations WHERE session_id = 'ses-migration-test'`,
	)
	if err != nil {
		t.Fatalf("query new columns: %v — columns not yet added by migrate() (expected red)", err)
	}
	defer nullRows.Close()

	for nullRows.Next() {
		var syncID string
		var reviewAfter, expiresAt *string
		if err := nullRows.Scan(&syncID, &reviewAfter, &expiresAt); err != nil {
			t.Fatalf("scan new columns: %v", err)
		}
		if reviewAfter != nil {
			t.Errorf("row %s: review_after = %q, want NULL for pre-existing row", syncID, *reviewAfter)
		}
		if expiresAt != nil {
			t.Errorf("row %s: expires_at = %q, want NULL for pre-existing row", syncID, *expiresAt)
		}
	}
	if err := nullRows.Err(); err != nil {
		t.Fatalf("nullRows.Err: %v", err)
	}

	// Assert memory_relations table exists.
	// This will fail (red) until Phase B adds the table to migrate().
	var tableName string
	err = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_relations table not found: %v — table not yet created by migrate() (expected red)", err)
	}
	if tableName != "memory_relations" {
		t.Errorf("memory_relations table name = %q, want 'memory_relations'", tableName)
	}
}

// ─── A.4: TestMigrate_Idempotent ────────────────────────────────────────────
//
// RED test: calls migrate() twice on the same DB (indirectly via New()) and
// asserts the schema is identical after both runs.  Also asserts that the
// new columns and memory_relations table exist after both runs.
//
// This test FAILS (red) until Phase B adds the new DDL to migrate().

func TestMigrate_Idempotent(t *testing.T) {
	// First run is done inside newTestStoreWithLegacySchema via New().
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)
	dir := s.cfg.DataDir

	// Close the store so we can re-open it.
	if err := s.Close(); err != nil {
		t.Fatalf("close store before second migration: %v", err)
	}

	// Second run: open the same DB again — migrate() will run a second time.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("New() second run failed: %v — migrate() is not idempotent", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	// Assert memory_relations still exists after the second run.
	var tableName string
	err = s2.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_relations not found after second migrate: %v (expected red)", err)
	}

	// Assert all 5 fixture rows are still intact after the second migration.
	var count int
	if err := s2.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE session_id = 'ses-migration-test'`,
	).Scan(&count); err != nil {
		t.Fatalf("count fixture rows after second migrate: %v", err)
	}
	if count != len(fixtures) {
		t.Errorf("after second migrate: expected %d rows, got %d", len(fixtures), count)
	}

	// Assert new columns still present and queryable.
	_, err = s2.db.Query(
		`SELECT review_after, expires_at FROM observations LIMIT 1`,
	)
	if err != nil {
		t.Fatalf("new columns missing after second migrate: %v (expected red)", err)
	}
}

// ─── A.5: TestMigrate_DoesNotTouchFTS5OrSyncMutations ───────────────────────
//
// RED test: asserts that after migrate() runs on a legacy DB:
//   - obs_fts (observations_fts) virtual table still exists and is queryable
//   - sync_mutations table still exists and any pre-existing rows are intact
//   - memory_relations table is present (new, expected)
//
// The test is primarily a regression guard: migrate() must not accidentally
// drop or corrupt the FTS5 virtual table or the sync_mutations table.
//
// This test FAILS (red) on the memory_relations assertion until Phase B.

func TestMigrate_DoesNotTouchFTS5OrSyncMutations(t *testing.T) {
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)

	// 1. observations_fts virtual table must still exist and return results.
	ftsRows, err := s.db.Query(
		`SELECT rowid FROM observations_fts WHERE observations_fts MATCH 'sessions'`,
	)
	if err != nil {
		t.Fatalf("observations_fts query failed after migrate: %v", err)
	}
	ftsRows.Close()

	// 2. observations_fts must be searchable with a fixture title term.
	var ftsCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH '"tokenizer"'`,
	).Scan(&ftsCount); err != nil {
		t.Fatalf("FTS5 match query: %v", err)
	}
	if ftsCount < 1 {
		t.Errorf("observations_fts: expected at least 1 match for 'tokenizer', got %d", ftsCount)
	}

	// 3. sync_mutations table must still exist (schema check via sqlite_master).
	var syncMutName string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sync_mutations'`,
	).Scan(&syncMutName); err != nil {
		t.Fatalf("sync_mutations table missing after migrate: %v", err)
	}
	if syncMutName != "sync_mutations" {
		t.Errorf("sync_mutations table name = %q, want 'sync_mutations'", syncMutName)
	}

	// 4. sync_mutations schema must have the expected columns.
	smRows, err := s.db.Query(`PRAGMA table_info(sync_mutations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(sync_mutations): %v", err)
	}
	defer smRows.Close()

	var smCols []string
	for smRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultVal any
		var pk int
		if err := smRows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan sync_mutations column: %v", err)
		}
		smCols = append(smCols, name)
	}
	if err := smRows.Err(); err != nil {
		t.Fatalf("smRows.Err: %v", err)
	}

	requiredSMCols := []string{"seq", "target_key", "entity", "entity_key", "op", "payload", "source", "project", "occurred_at", "acked_at"}
	colSet := make(map[string]bool, len(smCols))
	for _, c := range smCols {
		colSet[c] = true
	}
	for _, req := range requiredSMCols {
		if !colSet[req] {
			t.Errorf("sync_mutations missing expected column %q after migrate", req)
		}
	}

	// 5. memory_relations table MUST exist after migrate() (red until Phase B).
	var mrName string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&mrName); err != nil {
		t.Fatalf("memory_relations not found after migrate: %v (expected red until Phase B)", err)
	}

	// 6. memory_relations must NOT be in sync_mutations (REQ-009).
	// This is a forward-looking assertion; passes trivially until Phase C adds SaveRelation.
	var relInSync int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = 'memory_relation'`,
	).Scan(&relInSync); err != nil {
		t.Fatalf("query sync_mutations for memory_relation entity: %v", err)
	}
	if relInSync != 0 {
		t.Errorf("sync_mutations contains %d memory_relation rows, want 0 (REQ-009)", relInSync)
	}

	// 7. memory_relations columns must match the design DDL.
	// This assertion is also red until Phase B adds the table.
	mrColRows, err := s.db.Query(`PRAGMA table_info(memory_relations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(memory_relations): %v", err)
	}
	defer mrColRows.Close()

	var mrCols []string
	for mrColRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultVal any
		var pk int
		if err := mrColRows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan memory_relations column: %v", err)
		}
		mrCols = append(mrCols, name)
	}
	if err := mrColRows.Err(); err != nil {
		t.Fatalf("mrColRows.Err: %v", err)
	}

	requiredMRCols := []string{
		"id", "sync_id", "source_id", "target_id", "relation",
		"reason", "evidence", "confidence", "judgment_status",
		"marked_by_actor", "marked_by_kind", "marked_by_model",
		"session_id", "superseded_at", "superseded_by_relation_id",
		"created_at", "updated_at",
	}
	mrColSet := make(map[string]bool, len(mrCols))
	for _, c := range mrCols {
		mrColSet[c] = true
	}
	var missing []string
	for _, req := range requiredMRCols {
		if !mrColSet[req] {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		t.Errorf("memory_relations missing columns: %s (expected red until Phase B)", strings.Join(missing, ", "))
	}
}
