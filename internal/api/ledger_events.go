package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/centopw/nodr/internal/operations"
)

// ledgerEventSource is the CloudEvents source attribute of every ledger event.
const ledgerEventSource = "/api/v1"

// ledgerCloudEvent is the CloudEvents JSON envelope sent as SSE data. It is an
// invalidation signal only: clients re-fetch the named resources and never
// trust the payload for plan or state detail.
type ledgerCloudEvent struct {
	SpecVersion string               `json:"specversion"`
	Type        string               `json:"type"`
	Source      string               `json:"source"`
	ID          string               `json:"id"`
	Time        string               `json:"time"`
	Data        ledgerEventReference `json:"data"`
}

// ledgerEventReference names the changeset and operation an event concerns.
type ledgerEventReference struct {
	ChangesetID string `json:"changesetId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
}

// ledgerEventType maps an internal operations event type to its public
// CloudEvents type. The second result is false for internal types that are
// not exposed on the stream.
func ledgerEventType(internal string) (string, bool) {
	switch internal {
	case "operation.running":
		return "nodr.operation.started", true
	case "operation.succeeded":
		return "nodr.operation.succeeded", true
	case "operation.failed":
		return "nodr.operation.failed", true
	case "operation.created":
		return "nodr.changeset.changed", true
	}
	if strings.HasPrefix(internal, "changeset.") {
		return "nodr.changeset.changed", true
	}
	return "", false
}

// ledgerFrame renders one SSE frame for event, or reports false when the
// event type is not exposed.
func ledgerFrame(event operations.Event) (string, bool, error) {
	eventType, ok := ledgerEventType(event.Type)
	if !ok {
		return "", false, nil
	}
	envelope := ledgerCloudEvent{
		SpecVersion: "1.0",
		Type:        eventType,
		Source:      ledgerEventSource,
		ID:          strconv.FormatInt(event.ID, 10),
		Time:        event.CreatedAt.UTC().Format(time.RFC3339Nano),
		Data:        ledgerEventReference{ChangesetID: event.ChangesetID, OperationID: event.OperationID},
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", false, fmt.Errorf("api: encode ledger event %d: %w", event.ID, err)
	}
	return fmt.Sprintf("id: %d\nevent: message\ndata: %s\n\n", event.ID, data), true, nil
}

// events handles GET /events as a Server-Sent Events stream. It subscribes
// before replaying so no event committed during replay is lost; live events
// already covered by the replay are skipped by ID.
func (l *ledgerAPI) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "Internal server error", "streaming is not supported", nil)
		return
	}
	var lastID int64
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			lastID = id
		}
	}

	live, cancel := l.svc.Subscribe()
	defer cancel()

	replay, err := l.svc.EventsAfter(r.Context(), lastID)
	if err != nil {
		writeLedgerError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for _, event := range replay {
		if err := writeLedgerFrame(w, flusher, event); err != nil {
			return
		}
		lastID = max(lastID, event.ID)
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-live:
			if !ok {
				return
			}
			if event.ID <= lastID {
				continue
			}
			if err := writeLedgerFrame(w, flusher, event); err != nil {
				return
			}
			lastID = event.ID
		}
	}
}

// writeLedgerFrame writes event to w and flushes it. Events with unexposed
// types are skipped without error.
func writeLedgerFrame(w http.ResponseWriter, flusher http.Flusher, event operations.Event) error {
	frame, ok, err := ledgerFrame(event)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err := fmt.Fprint(w, frame); err != nil {
		return fmt.Errorf("api: write ledger event: %w", err)
	}
	flusher.Flush()
	return nil
}
