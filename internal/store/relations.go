package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// ─── Relation vocabulary (locked) ─────────────────────────────────────────────

// Valid relation type values. Type compatibility is NOT enforced in Phase 1;
// the agent does that judgment.
const (
	RelationPending      = "pending"
	RelationRelated      = "related"
	RelationCompatible   = "compatible"
	RelationScoped       = "scoped"
	RelationConflictsWith = "conflicts_with"
	RelationSupersedes   = "supersedes"
	RelationNotConflict  = "not_conflict"
)

// Valid judgment_status values.
const (
	JudgmentStatusPending  = "pending"
	JudgmentStatusJudged   = "judged"
	JudgmentStatusOrphaned = "orphaned"
	JudgmentStatusIgnored  = "ignored"
)

// validRelationVerbs is the locked set of relation verbs that mem_judge accepts.
// "pending" is NOT in this set — it is the default, not a verdict.
var validRelationVerbs = map[string]bool{
	RelationRelated:       true,
	RelationCompatible:    true,
	RelationScoped:        true,
	RelationConflictsWith: true,
	RelationSupersedes:    true,
	RelationNotConflict:   true,
}

// isValidRelationVerb returns true if v is an accepted mem_judge relation verb.
func isValidRelationVerb(v string) bool {
	return validRelationVerbs[v]
}

// ─── Types ────────────────────────────────────────────────────────────────────

// CandidateOptions controls the FindCandidates query.
type CandidateOptions struct {
	// Project filters candidates to the same project as the saved observation.
	Project string
	// Scope filters candidates to the same scope as the saved observation.
	Scope string
	// Type is reserved for Phase 2 type-compatibility filtering; NOT enforced Phase 1.
	Type string
	// Limit caps the number of candidates returned. Default 3 when nil or <=0.
	Limit int
	// BM25Floor is the minimum BM25 score (negative; closer to 0 = better match).
	// Candidates below the floor are excluded. Default -2.0 when nil.
	//
	// Use a pointer so that an explicit 0.0 (very strict — nothing passes) is
	// distinguishable from the zero value (which previously collided with the
	// default sentinel). nil means "use the default (-2.0)".
	BM25Floor *float64
}

// Candidate represents a potential conflict candidate surfaced by FindCandidates.
type Candidate struct {
	// ID is the integer primary key of the candidate observation.
	ID int64
	// SyncID is the TEXT sync_id of the candidate observation.
	SyncID string
	// Title is the candidate's title.
	Title string
	// Type is the candidate's observation type.
	Type string
	// TopicKey is the candidate's topic_key (may be nil).
	TopicKey *string
	// Score is the FTS5 BM25 rank (negative; closer to 0 = better match).
	Score float64
	// JudgmentID is the sync_id of the pending memory_relations row created
	// for this (source, candidate) pair.
	JudgmentID string
}

// Relation represents a row in memory_relations.
type Relation struct {
	ID                    int64    `json:"id"`
	SyncID                string   `json:"sync_id"`
	SourceID              string   `json:"source_id"`
	TargetID              string   `json:"target_id"`
	Relation              string   `json:"relation"`
	Reason                *string  `json:"reason,omitempty"`
	Evidence              *string  `json:"evidence,omitempty"`
	Confidence            *float64 `json:"confidence,omitempty"`
	JudgmentStatus        string   `json:"judgment_status"`
	MarkedByActor         *string  `json:"marked_by_actor,omitempty"`
	MarkedByKind          *string  `json:"marked_by_kind,omitempty"`
	MarkedByModel         *string  `json:"marked_by_model,omitempty"`
	SessionID             *string  `json:"session_id,omitempty"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
}

// ObservationRelations groups relations for a single observation, split by role.
type ObservationRelations struct {
	// AsSource holds relations where this observation is source_id.
	AsSource []Relation
	// AsTarget holds relations where this observation is target_id.
	AsTarget []Relation
}

// SaveRelationParams holds the inputs for SaveRelation.
type SaveRelationParams struct {
	// SyncID is the unique identifier for this relation row (format: rel-<16hex>).
	SyncID   string
	// SourceID is the TEXT sync_id of the source observation.
	SourceID string
	// TargetID is the TEXT sync_id of the target observation.
	TargetID string
}

// JudgeRelationParams holds the inputs for JudgeRelation.
type JudgeRelationParams struct {
	// JudgmentID is the sync_id of the relation row to update (required).
	JudgmentID    string
	// Relation is the verdict verb (required); must be one of validRelationVerbs.
	Relation      string
	// Reason is an optional free-text explanation.
	Reason        *string
	// Evidence is optional free-form JSON or text evidence.
	Evidence      *string
	// Confidence is optional 0..1 confidence score.
	Confidence    *float64
	// MarkedByActor is the actor identifier (e.g. "agent:claude-sonnet-4-6" or "user").
	MarkedByActor string
	// MarkedByKind is the actor kind ("agent", "human", "system").
	MarkedByKind  string
	// MarkedByModel is the model ID (may be empty for human actors).
	MarkedByModel string
	// SessionID is the session in which the judgment was made (optional).
	SessionID     string
}

// ─── FindCandidates ───────────────────────────────────────────────────────────

// FindCandidates runs a post-transaction FTS5 candidate query for the given
// savedID and returns at most opts.Limit candidates above the BM25 floor.
//
// For each candidate, a pending memory_relations row is inserted and the row's
// sync_id is exposed as Candidate.JudgmentID.
//
// Errors from this method are expected to be logged and swallowed by callers —
// detection failure must never fail the originating save.
func (s *Store) FindCandidates(savedID int64, opts CandidateOptions) ([]Candidate, error) {
	// Apply defaults.
	limit := opts.Limit
	if limit <= 0 {
		limit = 3
	}
	// BM25Floor uses pointer semantics: nil means "use the default (-2.0)".
	// An explicit pointer value (including 0.0) is used as-is.
	floor := -2.0
	if opts.BM25Floor != nil {
		floor = *opts.BM25Floor
	}

	// Get the saved observation to build the FTS query and for project/scope filtering.
	var title, project, scope string
	err := s.db.QueryRow(
		`SELECT title, ifnull(project,''), scope FROM observations WHERE id = ?`, savedID,
	).Scan(&title, &project, &scope)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("FindCandidates: observation %d not found", savedID)
	}
	if err != nil {
		return nil, fmt.Errorf("FindCandidates: get saved observation: %w", err)
	}

	// Use caller-supplied project/scope if provided (override from observation columns).
	if opts.Project != "" {
		project = opts.Project
	}
	if opts.Scope != "" {
		scope = opts.Scope
	}

	ftsQuery := sanitizeFTSCandidates(title)
	if ftsQuery == "" {
		return nil, nil
	}

	// FTS5 query: same project, same scope, exclude just-saved row, exclude soft-deleted.
	// BM25 floor filtering is done in Go after scanning.
	rows, err := s.db.Query(`
		SELECT o.id, ifnull(o.sync_id,'') as sync_id, o.title, o.type, o.topic_key,
		       fts.rank
		FROM observations_fts fts
		JOIN observations o ON o.id = fts.rowid
		WHERE observations_fts MATCH ?
		  AND o.id != ?
		  AND o.deleted_at IS NULL
		  AND ifnull(o.project,'') = ifnull(?,'')
		  AND o.scope = ?
		ORDER BY fts.rank
		LIMIT ?
	`, ftsQuery, savedID, project, scope, limit*3) // fetch extra rows to allow floor filtering
	if err != nil {
		return nil, fmt.Errorf("FindCandidates: FTS5 query: %w", err)
	}
	defer rows.Close()

	type rawCandidate struct {
		id       int64
		syncID   string
		title    string
		obsType  string
		topicKey *string
		score    float64
	}

	var raw []rawCandidate
	for rows.Next() {
		var rc rawCandidate
		if err := rows.Scan(&rc.id, &rc.syncID, &rc.title, &rc.obsType, &rc.topicKey, &rc.score); err != nil {
			return nil, fmt.Errorf("FindCandidates: scan: %w", err)
		}
		// Apply BM25 floor filter. BM25 scores are negative; closer to 0 = better.
		// We only include rows whose score >= floor (e.g., -1.5 >= -2.0).
		if rc.score < floor {
			continue
		}
		raw = append(raw, rc)
		if len(raw) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("FindCandidates: rows error: %w", err)
	}

	if len(raw) == 0 {
		return nil, nil
	}

	// Get the source observation's sync_id for the relation source_id.
	var sourceSyncID string
	if err := s.db.QueryRow(
		`SELECT ifnull(sync_id,'') FROM observations WHERE id = ?`, savedID,
	).Scan(&sourceSyncID); err != nil {
		return nil, fmt.Errorf("FindCandidates: get source sync_id: %w", err)
	}

	// Insert a pending relation row for each candidate.
	candidates := make([]Candidate, 0, len(raw))
	for _, rc := range raw {
		judgmentID := newSyncID("rel")
		_, err := s.db.Exec(`
			INSERT INTO memory_relations
				(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))
		`, judgmentID, sourceSyncID, rc.syncID)
		if err != nil {
			// Log and skip — don't fail the whole detection.
			continue
		}
		candidates = append(candidates, Candidate{
			ID:         rc.id,
			SyncID:     rc.syncID,
			Title:      rc.title,
			Type:       rc.obsType,
			TopicKey:   rc.topicKey,
			Score:      rc.score,
			JudgmentID: judgmentID,
		})
	}

	return candidates, nil
}

// ─── SaveRelation ─────────────────────────────────────────────────────────────

// SaveRelation inserts a new pending relation row. The SyncID field must be
// unique (enforced by the UNIQUE constraint on memory_relations.sync_id).
func (s *Store) SaveRelation(p SaveRelationParams) (*Relation, error) {
	_, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))
	`, p.SyncID, p.SourceID, p.TargetID)
	if err != nil {
		return nil, fmt.Errorf("SaveRelation: insert: %w", err)
	}
	return s.GetRelation(p.SyncID)
}

// ─── GetRelation ──────────────────────────────────────────────────────────────

// GetRelation retrieves a single relation row by its sync_id.
func (s *Store) GetRelation(syncID string) (*Relation, error) {
	row := s.db.QueryRow(`
		SELECT id, sync_id,
		       ifnull(source_id,''), ifnull(target_id,''),
		       relation, reason, evidence, confidence, judgment_status,
		       marked_by_actor, marked_by_kind, marked_by_model,
		       session_id, created_at, updated_at
		FROM memory_relations
		WHERE sync_id = ?
	`, syncID)

	var r Relation
	var sourceID, targetID string
	if err := row.Scan(
		&r.ID, &r.SyncID,
		&sourceID, &targetID,
		&r.Relation, &r.Reason, &r.Evidence, &r.Confidence, &r.JudgmentStatus,
		&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
		&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
	); err == sql.ErrNoRows {
		return nil, fmt.Errorf("GetRelation: relation %q not found", syncID)
	} else if err != nil {
		return nil, fmt.Errorf("GetRelation: %w", err)
	}
	r.SourceID = sourceID
	r.TargetID = targetID
	return &r, nil
}

// ─── JudgeRelation ────────────────────────────────────────────────────────────

// JudgeRelation records a verdict on an existing pending relation row.
//
// Re-judge policy: OVERWRITE the existing row (design decision). The updated row
// is returned on success.
//
// Returns an error if the judgment_id is unknown or the relation verb is invalid.
func (s *Store) JudgeRelation(p JudgeRelationParams) (*Relation, error) {
	if !isValidRelationVerb(p.Relation) {
		return nil, fmt.Errorf("JudgeRelation: invalid relation verb %q — must be one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict", p.Relation)
	}

	// Verify the relation exists.
	var exists int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE sync_id = ?`, p.JudgmentID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("JudgeRelation: check existence: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("JudgeRelation: relation %q not found", p.JudgmentID)
	}

	// Build nullable model string.
	var markedByModel *string
	if p.MarkedByModel != "" {
		markedByModel = &p.MarkedByModel
	}
	var sessionID *string
	if p.SessionID != "" {
		sessionID = &p.SessionID
	}

	_, err := s.db.Exec(`
		UPDATE memory_relations
		SET relation        = ?,
		    reason          = ?,
		    evidence        = ?,
		    confidence      = ?,
		    judgment_status = 'judged',
		    marked_by_actor = ?,
		    marked_by_kind  = ?,
		    marked_by_model = ?,
		    session_id      = ?,
		    updated_at      = datetime('now')
		WHERE sync_id = ?
	`,
		p.Relation,
		p.Reason,
		p.Evidence,
		p.Confidence,
		p.MarkedByActor,
		p.MarkedByKind,
		markedByModel,
		sessionID,
		p.JudgmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("JudgeRelation: update: %w", err)
	}

	return s.GetRelation(p.JudgmentID)
}

// ─── GetRelationsForObservations ──────────────────────────────────────────────

// GetRelationsForObservations returns a map of observation sync_id →
// ObservationRelations for all observations in syncIDs. Relations with
// judgment_status='orphaned' are excluded.
//
// A single SQL query with IN/OR avoids N+1 queries.
func (s *Store) GetRelationsForObservations(syncIDs []string) (map[string]ObservationRelations, error) {
	if len(syncIDs) == 0 {
		return map[string]ObservationRelations{}, nil
	}

	// Build IN clause.
	placeholders := make([]string, len(syncIDs))
	args := make([]any, 0, len(syncIDs)*2)
	for i, id := range syncIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	for _, id := range syncIDs {
		args = append(args, id)
	}

	inClause := joinStrings(placeholders, ",")
	query := fmt.Sprintf(`
		SELECT id, sync_id,
		       ifnull(source_id,''), ifnull(target_id,''),
		       relation, reason, evidence, confidence, judgment_status,
		       marked_by_actor, marked_by_kind, marked_by_model,
		       session_id, created_at, updated_at
		FROM memory_relations
		WHERE (source_id IN (%s) OR target_id IN (%s))
		  AND judgment_status != 'orphaned'
	`, inClause, inClause)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("GetRelationsForObservations: query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]ObservationRelations)

	for rows.Next() {
		var r Relation
		var sourceID, targetID string
		if err := rows.Scan(
			&r.ID, &r.SyncID,
			&sourceID, &targetID,
			&r.Relation, &r.Reason, &r.Evidence, &r.Confidence, &r.JudgmentStatus,
			&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
			&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("GetRelationsForObservations: scan: %w", err)
		}
		r.SourceID = sourceID
		r.TargetID = targetID

		// Index by source_id.
		for _, id := range syncIDs {
			if r.SourceID == id {
				entry := result[id]
				entry.AsSource = append(entry.AsSource, r)
				result[id] = entry
			}
			if r.TargetID == id {
				entry := result[id]
				entry.AsTarget = append(entry.AsTarget, r)
				result[id] = entry
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetRelationsForObservations: rows error: %w", err)
	}

	return result, nil
}

// sanitizeFTSCandidates builds an OR-based FTS5 query from a title so that
// FindCandidates returns documents with ANY term overlap (not all terms).
// Using implicit AND (sanitizeFTS) is too strict for candidate detection:
// the full saved title would require every word to appear in candidates.
// OR semantics give broader recall; BM25 score still captures relevance.
func sanitizeFTSCandidates(title string) string {
	words := strings.Fields(title)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, `"`)
		if w != "" {
			quoted = append(quoted, `"`+w+`"`)
		}
	}
	return strings.Join(quoted, " OR ")
}

// joinStrings joins a slice of strings with the given separator.
func joinStrings(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for _, item := range items[1:] {
		result += sep + item
	}
	return result
}
