package storage

// This is a read projection of the immutable event journal, not a reconstructed
// message transcript. Edits, deletions and receipts still count as events.
// Its head follows the existing order: greatest time, then smallest event ID.
const chatSummarySchema = `CREATE TABLE IF NOT EXISTS chat_summaries(connection_id TEXT NOT NULL,peer_key TEXT NOT NULL,last_event_id TEXT NOT NULL,last_event_time INTEGER NOT NULL,event_count INTEGER NOT NULL CHECK(event_count>0),PRIMARY KEY(connection_id,peer_key),FOREIGN KEY(connection_id,last_event_id) REFERENCES events(connection_id,id))`
const chatSummaryIndex = `CREATE INDEX IF NOT EXISTS chat_summaries_order ON chat_summaries(connection_id,last_event_time DESC,last_event_id)`

const rebuildChatSummaries = `INSERT INTO chat_summaries(connection_id,peer_key,last_event_id,last_event_time,event_count)
 SELECT connection_id,peer_key,id,event_time,total FROM (
 SELECT connection_id,peer_key,id,event_time,COUNT(*) OVER(PARTITION BY connection_id,peer_key) AS total,
 ROW_NUMBER() OVER(PARTITION BY connection_id,peer_key ORDER BY event_time DESC,id) AS position
 FROM events WHERE peer_key<>':') WHERE position=1
 ON CONFLICT(connection_id,peer_key) DO UPDATE SET last_event_id=excluded.last_event_id,last_event_time=excluded.last_event_time,event_count=excluded.event_count`

const updateChatSummary = `INSERT INTO chat_summaries(connection_id,peer_key,last_event_id,last_event_time,event_count) VALUES(?,?,?,?,1)
 ON CONFLICT(connection_id,peer_key) DO UPDATE SET event_count=chat_summaries.event_count+1,
 last_event_id=CASE WHEN excluded.last_event_time>chat_summaries.last_event_time OR (excluded.last_event_time=chat_summaries.last_event_time AND excluded.last_event_id<chat_summaries.last_event_id) THEN excluded.last_event_id ELSE chat_summaries.last_event_id END,
 last_event_time=MAX(chat_summaries.last_event_time,excluded.last_event_time)`

const listChatsQuery = `SELECT e.body,h.event_count FROM chat_summaries h
 JOIN events e ON e.connection_id=h.connection_id AND e.id=h.last_event_id
 WHERE h.connection_id=? ORDER BY h.last_event_time DESC,h.last_event_id LIMIT ? OFFSET ?`

const eventPeerOrderIndex = `CREATE INDEX events_chat ON events(connection_id,peer_key,event_time DESC,id)`
