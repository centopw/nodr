package operations

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

const subscriberBuffer = 8

func appendEvent(ctx context.Context, tx *sql.Tx, changesetID, operationID, eventType string) (Event, error) {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO events (changeset_id, operation_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)`, nullable(changesetID), nullable(operationID), eventType, []byte{}, now.UnixNano())
	if err != nil {
		return Event{}, fmt.Errorf("operations: append event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Event{}, fmt.Errorf("operations: event ID: %w", err)
	}
	return Event{ID: id, ChangesetID: changesetID, OperationID: operationID, Type: eventType, CreatedAt: now}, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) publish(event Event) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(s.subs, ch)
		}
	}
}

// Subscribe receives events committed after subscription. The returned
// cancellation function is safe to call more than once.
func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	s.subsMu.Lock()
	if s.subs != nil {
		s.subs[ch] = struct{}{}
	} else {
		close(ch)
	}
	s.subsMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.subsMu.Lock()
			if _, ok := s.subs[ch]; ok {
				delete(s.subs, ch)
				close(ch)
			}
			s.subsMu.Unlock()
		})
	}
}

// EventsAfter returns persisted events with IDs greater than afterID.
func (s *Store) EventsAfter(ctx context.Context, afterID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, changeset_id, operation_id, type, data, created_at FROM events WHERE id > ? ORDER BY id`, afterID)
	if err != nil {
		return nil, fmt.Errorf("operations: list events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var changesetID, operationID sql.NullString
		var createdAt int64
		if err := rows.Scan(&event.ID, &changesetID, &operationID, &event.Type, &event.Data, &createdAt); err != nil {
			return nil, fmt.Errorf("operations: scan event: %w", err)
		}
		event.ChangesetID = changesetID.String
		event.OperationID = operationID.String
		event.CreatedAt = time.Unix(0, createdAt).UTC()
		event.Data = append([]byte(nil), event.Data...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("operations: iterate events: %w", err)
	}
	return events, nil
}
