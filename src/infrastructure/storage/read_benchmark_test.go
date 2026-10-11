package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers"
)

// This fixture deliberately retains a large journal. Setup is outside the timer;
// the benchmark measures the public read rather than event/provider generation.
func BenchmarkListChatsRetainedJournal(b *testing.B) {
	for _, size := range []int{10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			ctx := context.Background()
			s, err := Open(filepath.Join(b.TempDir(), "read.db"), testKey, providers.Contracts())
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			d, err := s.CreateDevice(ctx, "synthetic-read", domains.ProviderBale)
			if err != nil {
				b.Fatal(err)
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			statement, err := tx.PrepareContext(ctx, `INSERT INTO events(id,connection_id,peer_key,type,message_id,event_time,body,created_at) VALUES(?,?,?,'message',?,?,?,?)`)
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < size; i++ {
				id := fmt.Sprintf("synthetic-%09d", i)
				e := domains.Event{ID: id, Provider: d.Provider, Type: "message", Peer: domains.Peer{Type: "user", ID: fmt.Sprint(i%100 + 1)}, Time: time.UnixMilli(int64(i)), Payload: json.RawMessage(`{"kind":"text","message":"synthetic"}`)}
				body, _ := marshal(e)
				if _, err = statement.ExecContext(ctx, id, d.ConnectionID, e.Peer.Key(), id, i, body, i); err != nil {
					b.Fatal(err)
				}
			}
			statement.Close()
			if err = tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if _, err = s.db.ExecContext(ctx, rebuildChatSummaries); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				chats, err := s.ListChats(ctx, d.ConnectionID, 50, 0)
				if err != nil || len(chats) != 50 {
					b.Fatalf("read: %d chats, %v", len(chats), err)
				}
			}
		})
	}
}
