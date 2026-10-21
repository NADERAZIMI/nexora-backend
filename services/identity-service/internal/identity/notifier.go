package identity

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// NotificationRequest is a one-time verification or reset message. Token is
// plaintext only in memory and is never logged.
type NotificationRequest struct {
	UserID      uuid.UUID
	Channel     string
	Kind        string
	Destination string
	Token       string
}

// Notifier queues or delivers a notification. The identity service must not
// claim delivery: OutboxNotifier persists an encrypted pending item for the
// notification service.
type Notifier interface {
	Send(ctx context.Context, request NotificationRequest) error
}

// OutboxNotifier encrypts notification data at rest and inserts it into the
// identity notification outbox. No network delivery happens here.
type OutboxNotifier struct {
	store         Store
	piiCipher     SecretCipher
	payloadCipher SecretCipher
}

// NewOutboxNotifier constructs an encrypted outbox notifier.
func NewOutboxNotifier(store Store, piiCipher, payloadCipher SecretCipher) (*OutboxNotifier, error) {
	if store == nil || piiCipher == nil || payloadCipher == nil {
		return nil, errors.New("outbox notifier dependencies are incomplete")
	}
	return &OutboxNotifier{store: store, piiCipher: piiCipher, payloadCipher: payloadCipher}, nil
}

// Send encrypts and enqueues the notification.
func (n *OutboxNotifier) Send(ctx context.Context, request NotificationRequest) error {
	if request.UserID == uuid.Nil || request.Channel == "" || request.Kind == "" || request.Token == "" {
		return errors.New("notification request is incomplete")
	}
	destination, err := n.piiCipher.Encrypt([]byte(request.Destination))
	if err != nil {
		return err
	}
	payload, err := n.payloadCipher.Encrypt(mustJSON(map[string]string{"token": request.Token, "kind": request.Kind}))
	if err != nil {
		return err
	}
	return n.store.EnqueueNotification(ctx, &Notification{
		UserID: request.UserID, Channel: request.Channel, Kind: request.Kind,
		DestinationCiphertext: destination, PayloadCiphertext: payload, CreatedAt: time.Now().UTC(),
	})
}

// CaptureNotifier records requests for executable tests.
type CaptureNotifier struct {
	mu       sync.Mutex
	Requests []NotificationRequest
}

// Send captures a notification request.
func (n *CaptureNotifier) Send(_ context.Context, request NotificationRequest) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Requests = append(n.Requests, request)
	return nil
}

// Last returns the most recent captured request and whether one exists.
func (n *CaptureNotifier) Last() (NotificationRequest, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.Requests) == 0 {
		return NotificationRequest{}, false
	}
	return n.Requests[len(n.Requests)-1], true
}

// Find returns the first captured request matching kind.
func (n *CaptureNotifier) Find(kind string) (NotificationRequest, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, request := range n.Requests {
		if request.Kind == kind {
			return request, true
		}
	}
	return NotificationRequest{}, false
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
