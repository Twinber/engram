package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── Fakes for mutation tests ─────────────────────────────────────────────────

type fakeMutationStore struct {
	fakeStore
	mutations      []MutationEntry
	syncEnabledMap map[string]bool // project → sync enabled
	errInsert      error
	errList        error
}

func newFakeMutationStore() *fakeMutationStore {
	return &fakeMutationStore{
		fakeStore:      fakeStore{chunks: make(map[string][]byte)},
		syncEnabledMap: make(map[string]bool),
	}
}

func (s *fakeMutationStore) IsProjectSyncEnabled(project string) (bool, error) {
	if enabled, ok := s.syncEnabledMap[project]; ok {
		return enabled, nil
	}
	return true, nil // default: enabled
}

func (s *fakeMutationStore) InsertMutationBatch(ctx context.Context, batch []MutationEntry) ([]int64, error) {
	if s.errInsert != nil {
		return nil, s.errInsert
	}
	seqs := make([]int64, len(batch))
	for i := range batch {
		seq := int64(len(s.mutations) + i + 1)
		seqs[i] = seq
		s.mutations = append(s.mutations, batch[i])
	}
	return seqs, nil
}

func (s *fakeMutationStore) ListMutationsSince(ctx context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]StoredMutation, bool, int64, error) {
	if s.errList != nil {
		return nil, false, 0, s.errList
	}
	allowed := make(map[string]struct{})
	for _, p := range allowedProjects {
		allowed[p] = struct{}{}
	}
	// allowedProjects == nil means no enrollment filter; non-nil (even empty) means filter by enrollment.
	useFilter := allowedProjects != nil
	var all []StoredMutation
	for i, m := range s.mutations {
		seq := int64(i + 1)
		if seq <= sinceSeq {
			continue
		}
		if useFilter {
			if _, ok := allowed[m.Project]; !ok {
				continue
			}
		}
		all = append(all, StoredMutation{
			Seq:        seq,
			Project:    m.Project,
			Entity:     m.Entity,
			EntityKey:  m.EntityKey,
			Op:         m.Op,
			Payload:    m.Payload,
			OccurredAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	hasMore := false
	latestSeq := int64(0)
	if len(all) > limit {
		all = all[:limit]
		hasMore = true
	}
	if len(all) > 0 {
		latestSeq = all[len(all)-1].Seq
	}
	return all, hasMore, latestSeq, nil
}

// multiProjectAuth authorizes specific projects per token.
type multiProjectAuth struct {
	token    string
	projects []string // projects this token is enrolled in
}

func (a multiProjectAuth) Authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != a.token {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

func (a multiProjectAuth) AuthorizeProject(project string) error {
	for _, p := range a.projects {
		if p == project {
			return nil
		}
	}
	return fmt.Errorf("project %q not enrolled", project)
}

func (a multiProjectAuth) EnrolledProjects() []string {
	return a.projects
}

// ─── Push endpoint tests ─────────────────────────────────────────────────────

func TestMutationPushEndpointAccepted(t *testing.T) {
	// REQ-200 happy path: 5 entries → HTTP 200, accepted_seqs has 5 items
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(5, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		AcceptedSeqs []int64 `json:"accepted_seqs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.AcceptedSeqs) != 5 {
		t.Fatalf("expected 5 accepted_seqs, got %d", len(resp.AcceptedSeqs))
	}
}

func TestMutationPushEndpointUnauth(t *testing.T) {
	// REQ-200 missing token → 401
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	// No Authorization header
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushEndpointBatchTooLarge(t *testing.T) {
	// REQ-200: 101 entries → 400
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(101, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushEndpointEmptyBatch(t *testing.T) {
	// REQ-200 empty entries → 200 with accepted_seqs: []
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(0, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		AcceptedSeqs []int64 `json:"accepted_seqs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.AcceptedSeqs) != 0 {
		t.Fatalf("expected empty accepted_seqs, got %v", resp.AcceptedSeqs)
	}
}

// ─── Pull endpoint tests ──────────────────────────────────────────────────────

func TestMutationPullEndpointSinceSeq(t *testing.T) {
	// REQ-201: since_seq=5, 10 stored mutations → returns 5 (seqs 6–10)
	ms := newFakeMutationStore()
	// Pre-load 10 mutations
	for i := 0; i < 10; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=5&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
		LatestSeq int64             `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 5 {
		t.Fatalf("expected 5 mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
	if resp.LatestSeq != 10 {
		t.Fatalf("expected latest_seq=10, got %d", resp.LatestSeq)
	}
}

func TestMutationPullEndpointHasMore(t *testing.T) {
	// REQ-201: 150 mutations, limit=100 → has_more=true, 100 mutations returned
	ms := newFakeMutationStore()
	for i := 0; i < 150; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
		LatestSeq int64             `json:"latest_seq"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 100 {
		t.Fatalf("expected 100 mutations, got %d", len(resp.Mutations))
	}
	if !resp.HasMore {
		t.Fatal("expected has_more=true")
	}
}

func TestMutationPullEndpointUnauth(t *testing.T) {
	// REQ-201 missing token → 401
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	// No Authorization header
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPullEndpointBeyondLatest(t *testing.T) {
	// REQ-201: since_seq beyond latest → empty
	ms := newFakeMutationStore()
	for i := 0; i < 5; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("k%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=100&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected empty mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
}

// ─── Enrollment filter tests ──────────────────────────────────────────────────

func TestMutationPullEnrollmentFilter(t *testing.T) {
	// REQ-202: caller enrolled in "proj-a" only; both proj-a and proj-b exist
	ms := newFakeMutationStore()
	// Insert proj-a and proj-b mutations
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	// Caller only enrolled in proj-a
	srv := newMutationTestServer(ms, "token-a", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Mutations) != 3 {
		t.Fatalf("expected 3 mutations (proj-a only), got %d", len(resp.Mutations))
	}
}

func TestMutationPullCrossTenantLeak(t *testing.T) {
	// REQ-202: two callers, no cross-tenant leak
	ms := newFakeMutationStore()
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	srvA := newMutationTestServer(ms, "token-a", []string{"proj-a"})
	srvB := newMutationTestServer(ms, "token-b", []string{"proj-b"})

	// Caller A — should only see proj-a
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	reqA.Header.Set("Authorization", "Bearer token-a")
	srvA.Handler().ServeHTTP(recA, reqA)

	// Caller B — should only see proj-b
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	reqB.Header.Set("Authorization", "Bearer token-b")
	srvB.Handler().ServeHTTP(recB, reqB)

	if recA.Code != http.StatusOK || recB.Code != http.StatusOK {
		t.Fatalf("expected 200 for both, got A=%d B=%d", recA.Code, recB.Code)
	}

	var respA, respB struct {
		Mutations []struct {
			Project string `json:"project"`
		} `json:"mutations"`
	}
	_ = json.NewDecoder(recA.Body).Decode(&respA)
	_ = json.NewDecoder(recB.Body).Decode(&respB)

	for _, m := range respA.Mutations {
		if m.Project != "proj-a" {
			t.Fatalf("cross-tenant leak: caller-A received mutation for project %q", m.Project)
		}
	}
	for _, m := range respB.Mutations {
		if m.Project != "proj-b" {
			t.Fatalf("cross-tenant leak: caller-B received mutation for project %q", m.Project)
		}
	}
}

func TestMutationPullNoEnrollments(t *testing.T) {
	// REQ-202: no enrolled projects → empty 200
	ms := newFakeMutationStore()
	_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	})

	srv := newMutationTestServer(ms, "secret", []string{}) // no enrollments

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected empty mutations, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
}

// ─── Sync-pause tests (REQ-203) ───────────────────────────────────────────────

func TestMutationPushSyncPaused409(t *testing.T) {
	// REQ-203: sync_enabled=false → 409
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false // paused

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync-paused") {
		t.Fatalf("expected sync-paused error, got %q", rec.Body.String())
	}
}

func TestMutationPushNonPausedAccepted(t *testing.T) {
	// REQ-203: non-paused → 200
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = true

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMutationPushPausePerProject(t *testing.T) {
	// REQ-203: alpha paused, beta active
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false
	ms.syncEnabledMap["proj-b"] = true

	srvA := newMutationTestServer(ms, "secret", []string{"proj-a"})
	srvB := newMutationTestServer(ms, "secret", []string{"proj-b"})

	// proj-a should be rejected
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", marshalPushRequest(t, makeMutationEntries(1, "proj-a")))
	reqA.Header.Set("Authorization", "Bearer secret")
	reqA.Header.Set("Content-Type", "application/json")
	srvA.Handler().ServeHTTP(recA, reqA)

	if recA.Code != http.StatusConflict {
		t.Fatalf("proj-a: expected 409, got %d", recA.Code)
	}

	// proj-b should be accepted
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", marshalPushRequest(t, makeMutationEntries(1, "proj-b")))
	reqB.Header.Set("Authorization", "Bearer secret")
	reqB.Header.Set("Content-Type", "application/json")
	srvB.Handler().ServeHTTP(recB, reqB)

	if recB.Code != http.StatusOK {
		t.Fatalf("proj-b: expected 200, got %d", recB.Code)
	}
}

func TestMutationPushPauseAdminStillBlocked(t *testing.T) {
	// REQ-203: admin token still gets 409 when project is paused
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	// Admin token is set but project is still paused (pause is a data policy)
	srv := New(ms, fakeAuth{}, 0, WithDashboardAdminToken("admin-token"), WithProjectAuthorizer(multiProjectAuth{
		token:    "admin-token",
		projects: []string{"proj-a"},
	}))

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for admin with paused project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── BC2: Cross-project authorization bypass tests ────────────────────────────

// TestMutationPushRejectsUnauthorizedProject verifies BC2:
// A bearer token authorized for "proj-a" MUST NOT push mutations for "proj-b".
func TestMutationPushRejectsUnauthorizedProject(t *testing.T) {
	ms := newFakeMutationStore()
	// Token authorized only for "proj-a"
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	// But we send entries for "proj-b" — must be rejected
	entries := makeMutationEntries(2, "proj-b")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthorized project, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestMutationPushRejectsMixedProjectBatch verifies BC2:
// A batch containing both authorized and unauthorized projects MUST be entirely rejected.
func TestMutationPushRejectsMixedProjectBatch(t *testing.T) {
	ms := newFakeMutationStore()
	// Token authorized only for "proj-a"
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	// Batch contains both "proj-a" (authorized) and "proj-b" (unauthorized)
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "proj-b", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for mixed batch with unauthorized project, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Verify nothing was stored for proj-a either (all-or-nothing rejection)
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored on mixed-batch rejection, got %d", len(ms.mutations))
	}
}

// ─── BW2: Fail-closed enrollment filter tests ──────────────────────────────

// TestMutationPullFailsClosedWithoutEnrolledProjectsProvider verifies BW2:
// When projectAuth is non-nil but does NOT implement EnrolledProjectsProvider,
// allowedProjects must default to [] (empty, not nil), returning no mutations
// instead of leaking all projects.
func TestMutationPullFailsClosedWithoutEnrolledProjectsProvider(t *testing.T) {
	ms := newFakeMutationStore()
	// Insert mutations for several projects
	for i := 0; i < 3; i++ {
		_, _ = ms.InsertMutationBatch(context.Background(), []MutationEntry{
			{Project: "proj-a", Entity: "obs", EntityKey: fmt.Sprintf("ka%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
			{Project: "proj-b", Entity: "obs", EntityKey: fmt.Sprintf("kb%d", i), Op: "upsert", Payload: json.RawMessage(`{}`)},
		})
	}

	// Use a ProjectAuthorizer that does NOT implement EnrolledProjectsProvider
	authWithoutEnrollment := &simpleProjectAuth{token: "secret"}
	srv := New(ms, authWithoutEnrollment, 0, WithProjectAuthorizer(authWithoutEnrollment))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0&limit=100", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	var resp struct {
		Mutations []json.RawMessage `json:"mutations"`
		HasMore   bool              `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Must return empty — fail closed when enrollment provider is unavailable
	if len(resp.Mutations) != 0 {
		t.Fatalf("expected 0 mutations (fail-closed), got %d", len(resp.Mutations))
	}
}

// ─── BW9: 409 pause gate uses writeActionableError ────────────────────────────

// TestMutationPushPauseGives409WithActionableError verifies BW9:
// The sync-paused 409 response MUST use the structured error envelope
// (error_class, error_code, error fields), not a plain JSON body.
func TestMutationPushPauseGives409WithActionableError(t *testing.T) {
	ms := newFakeMutationStore()
	ms.syncEnabledMap["proj-a"] = false

	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})
	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	var resp struct {
		ErrorClass string `json:"error_class"`
		ErrorCode  string `json:"error_code"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ErrorClass == "" {
		t.Fatalf("expected error_class in 409 response, got empty; body=%q", rec.Body.String())
	}
	if resp.ErrorCode == "" {
		t.Fatalf("expected error_code in 409 response, got empty; body=%q", rec.Body.String())
	}
	if resp.Error == "" {
		t.Fatalf("expected error in 409 response, got empty; body=%q", rec.Body.String())
	}
}

// ─── BR2-1: Empty-project entry rejection ────────────────────────────────────

// TestMutationPushRejectsEmptyProjectEntries verifies BR2-1:
// Entries with an empty project field MUST be rejected with HTTP 400 before
// auth/pause checks — they must never be silently inserted into cloud_mutations.
func TestMutationPushRejectsEmptyProjectEntries(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := []MutationEntry{
		{Project: "", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty-project entry, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Ensure nothing was stored
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored for empty-project entry, got %d", len(ms.mutations))
	}
}

// TestMutationPushRejectsMixedEmptyProjectBatch verifies BR2-1 for batches:
// A batch with some empty and some valid projects must be rejected entirely.
func TestMutationPushRejectsMixedEmptyProjectBatch(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for batch with empty-project entry, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.mutations) != 0 {
		t.Fatalf("expected no mutations stored when batch contains empty-project entry, got %d", len(ms.mutations))
	}
}

// simpleProjectAuth implements Authenticator + ProjectAuthorizer but NOT EnrolledProjectsProvider.
// Used to test BW2 fail-closed behavior.
type simpleProjectAuth struct {
	token string
}

func (a *simpleProjectAuth) Authorize(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != a.token {
		return fmt.Errorf("unauthorized")
	}
	return nil
}

func (a *simpleProjectAuth) AuthorizeProject(_ string) error {
	return nil // allow all projects for this auth
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func newMutationTestServer(ms *fakeMutationStore, token string, projects []string) *CloudServer {
	auth := multiProjectAuth{token: token, projects: projects}
	return New(ms, auth, 0)
}

func makeMutationEntries(count int, project string) []MutationEntry {
	entries := make([]MutationEntry, count)
	for i := range entries {
		entries[i] = MutationEntry{
			Project:   project,
			Entity:    "observation",
			EntityKey: fmt.Sprintf("obs-%d", i),
			Op:        "upsert",
			Payload:   json.RawMessage(`{"title":"test"}`),
		}
	}
	return entries
}

func marshalPushRequest(t *testing.T, entries []MutationEntry) *bytes.Buffer {
	t.Helper()
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal push request: %v", err)
	}
	return bytes.NewBuffer(body)
}
