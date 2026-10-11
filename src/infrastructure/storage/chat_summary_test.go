package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers"
	"github.com/mimalef70/goomni/src/pkg/sqlite"
	"github.com/stretchr/testify/require"
)

// Compare with the old journal-derived query, including out-of-order events,
// equal timestamps, updates, deletes, duplicates and account isolation.
func TestChatSummaryMatchesJournalAndStablePages(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := device(t, s, "summary-one")
	b, err := s.CreateDevice(ctx, "summary-two", domains.ProviderEitaa)
	require.NoError(t, err)
	for _, d := range []domains.Device{a, b} {
		for i, kind := range []string{"message", "message.edited", "message.deleted", "message", "message.read"} {
			e := event("summary-"+kind+string(rune('a'+i)), "")
			e.Provider = d.Provider
			e.MessageID = "101"
			e.Type = kind
			e.Peer.ID = []string{"42", "42", "42", "43", "44"}[i]
			e.Time = time.UnixMilli([]int64{100, 200, 150, 200, 200}[i])
			_, err := s.AppendEvent(ctx, d.ConnectionID, e, nil)
			require.NoError(t, err)
			e.Time = time.UnixMilli(1000)
			e.Payload = json.RawMessage(`{"changed_duplicate":true}`)
			created, err := s.AppendEvent(ctx, d.ConnectionID, e, nil)
			require.NoError(t, err)
			require.False(t, created)
		}
		actual, err := s.ListChats(ctx, d.ConnectionID, 100, 0)
		require.NoError(t, err)
		rows, err := s.db.QueryContext(ctx, `SELECT body,total FROM (SELECT body,id,event_time,COUNT(*) OVER(PARTITION BY peer_key) total,ROW_NUMBER() OVER(PARTITION BY peer_key ORDER BY event_time DESC,id) position FROM events WHERE connection_id=? AND peer_key<>':') WHERE position=1 ORDER BY event_time DESC,id`, d.ConnectionID)
		require.NoError(t, err)
		var expected []domains.Chat
		for rows.Next() {
			var body string
			var chat domains.Chat
			require.NoError(t, rows.Scan(&body, &chat.Count))
			require.NoError(t, json.Unmarshal([]byte(body), &chat.LastEvent))
			chat.Peer = chat.LastEvent.Peer
			expected = append(expected, chat)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		require.Equal(t, expected, actual)
		var paged []domains.Chat
		for offset := range len(actual) {
			page, err := s.ListChats(ctx, d.ConnectionID, 1, offset)
			require.NoError(t, err)
			paged = append(paged, page...)
		}
		require.Equal(t, actual, paged)
	}
}

func TestChatSummaryFailureRollsBackEntireBatch(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "summary-rollback")
	_, err := s.db.Exec(`CREATE TRIGGER fail_chat_summary BEFORE INSERT ON chat_summaries BEGIN SELECT RAISE(FAIL,'synthetic failure'); END`)
	require.NoError(t, err)
	batch := domains.EventBatch{Events: []domains.Event{event("summary-failed", "")}, Checkpoints: []domains.CheckpointTransition{{Scope: domains.DefaultCheckpointScope, Next: "next"}}}
	n, err := s.AppendBatch(ctx, d.ConnectionID, batch, []domains.WebhookTarget{{URL: "https://synthetic.test/webhook", Secret: "synthetic"}})
	require.Error(t, err)
	require.Zero(t, n)
	for _, table := range []string{"events", "deliveries", "chat_summaries"} {
		var count int
		require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
	cp, err := s.ScopedCheckpoint(ctx, d.ConnectionID, domains.DefaultCheckpointScope)
	require.NoError(t, err)
	require.Empty(t, cp)
	_, err = s.db.Exec(`DROP TRIGGER fail_chat_summary`)
	require.NoError(t, err)
	n, err = s.AppendBatch(ctx, d.ConnectionID, batch, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	chats, err := s.ListChats(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	require.Len(t, chats, 1)
}

func TestSchemaEightChatSummaryMigrationPreservesAcceptedData(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	d := device(t, s, "summary-upgrade")
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "42", Token: "synthetic-session"}
	require.NoError(t, s.SaveSession(ctx, d.ConnectionID, session))
	op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("synthetic"), "summary-key", domains.AdmissionLimits{})
	require.NoError(t, err)
	_, err = s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "unknown"))
	_, err = s.AppendEvent(ctx, d.ConnectionID, event("summary-before-upgrade", "checkpoint"), []domains.WebhookTarget{{URL: "https://synthetic.test/webhook", Secret: "synthetic"}})
	require.NoError(t, err)
	before, err := s.ListChats(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	deliveries, err := s.ListDeliveries(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	_, err = s.db.Exec(`DROP TABLE chat_summaries`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=8`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey, providers.Contracts())
	require.NoError(t, err)
	defer s.Close()
	after, err := s.ListChats(ctx, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	require.Equal(t, before, after)
	restored, err := s.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, session.Token, restored.Token)
	unchanged, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", unchanged.State)
	require.Equal(t, op.Request.RequestID, unchanged.Request.RequestID)
	delivery, err := s.GetDelivery(ctx, d.ConnectionID, deliveries[0].ID)
	require.NoError(t, err)
	require.Equal(t, deliveries[0].Body, delivery.Body)
	cp, err := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "checkpoint", cp)
}

func TestChatReadUsesSummaryOrderIndex(t *testing.T) {
	s, _ := testStore(t)
	d := device(t, s, "summary-plan")
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+listChatsQuery, d.ConnectionID, 50, 0)
	require.NoError(t, err)
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var step string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &step))
		plan += step + "\n"
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan, "chat_summaries_order")
	require.NotContains(t, plan, "TEMP B-TREE")
}

func TestSchemaNineMigrationFailurePreservesSchemaEight(t *testing.T) {
	s, path := testStore(t)
	ctx := context.Background()
	d := device(t, s, "summary-migration-failure")
	_, err := s.AppendEvent(ctx, d.ConnectionID, event("synthetic-retained", ""), nil)
	require.NoError(t, err)
	for _, statement := range []string{
		`DROP TABLE chat_summaries`,
		`DROP INDEX events_chat`,
		`CREATE INDEX events_chat ON events(connection_id,peer_key,event_time DESC)`,
		`UPDATE gobale_meta SET version=8`,
		// A conflicting object forces failure after index replacement begins.
		`CREATE VIEW chat_summaries AS SELECT id FROM events`,
	} {
		_, err = s.db.Exec(statement)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	_, err = Open(path, testKey, providers.Contracts())
	require.ErrorContains(t, err, "migrate chat read projection")
	db, err := sql.Open(sqlite.DriverName, path)
	require.NoError(t, err)
	defer db.Close()
	var version, count int
	var index string
	require.NoError(t, db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, 8, version)
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='events_chat'`).Scan(&index))
	require.Equal(t, `CREATE INDEX events_chat ON events(connection_id,peer_key,event_time DESC)`, index)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE connection_id=?`, d.ConnectionID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPeerReadUsesStableTimeAndIDIndex(t *testing.T) {
	s, _ := testStore(t)
	d := device(t, s, "peer-read-plan")
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT body FROM events WHERE connection_id=? AND peer_key=? ORDER BY event_time DESC,id LIMIT ? OFFSET ?`, d.ConnectionID, "user:42", 50, 0)
	require.NoError(t, err)
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var step string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &step))
		plan += step + "\n"
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan, "events_chat")
	require.NotContains(t, plan, "TEMP B-TREE")
}
