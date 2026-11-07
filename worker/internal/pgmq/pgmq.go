// Package pgmq is a thin pgx client for the pgmq jobs queue: visibility-timeout
// reads, lease extension, archive, and dead-lettering.
package pgmq

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Message is one queue message. Body is the raw JSON as enqueued.
type Message struct {
	ID        int64
	ReadCount int32
	Body      []byte
}

// Client talks to a pgmq queue through a pgx pool.
type Client struct {
	pool *pgxpool.Pool
}

// New returns a Client for the given pool.
func New(pool *pgxpool.Pool) *Client {
	return &Client{pool: pool}
}

// Read reads up to qty messages from queue, hiding each read message for
// vtSeconds. A message that is not archived before the timeout becomes
// visible again (pgmq read_ct counts every read).
func (c *Client) Read(ctx context.Context, queue string, vtSeconds, qty int32) ([]Message, error) {
	rows, err := c.pool.Query(ctx,
		`select msg_id, read_ct, message::text from pgmq.read($1::text, $2::int, $3::int)`,
		queue, vtSeconds, qty)
	if err != nil {
		return nil, fmt.Errorf("pgmq.read %s: %w", queue, err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var body string
		if err := rows.Scan(&m.ID, &m.ReadCount, &body); err != nil {
			return nil, fmt.Errorf("pgmq.read %s: scan: %w", queue, err)
		}
		m.Body = []byte(body)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetVT extends (or shortens) a message's visibility timeout lease.
func (c *Client) SetVT(ctx context.Context, queue string, msgID int64, vtSeconds int32) error {
	var until any
	err := c.pool.QueryRow(ctx,
		`select pgmq.set_vt($1::text, $2::bigint, $3::int)`,
		queue, msgID, vtSeconds).Scan(&until)
	if err != nil {
		return fmt.Errorf("pgmq.set_vt %s %d: %w", queue, msgID, err)
	}
	return nil
}

// Archive removes a message from the queue for good (success path).
func (c *Client) Archive(ctx context.Context, queue string, msgID int64) error {
	var archived bool
	err := c.pool.QueryRow(ctx,
		`select pgmq.archive($1::text, $2::bigint)`,
		queue, msgID).Scan(&archived)
	if err != nil {
		return fmt.Errorf("pgmq.archive %s %d: %w", queue, msgID, err)
	}
	if !archived {
		return fmt.Errorf("pgmq.archive %s %d: message not found", queue, msgID)
	}
	return nil
}

// Send enqueues body (raw JSON) onto queue (used for dead-lettering).
func (c *Client) Send(ctx context.Context, queue string, body []byte) error {
	var msgID int64
	err := c.pool.QueryRow(ctx,
		`select pgmq.send($1::text, $2::jsonb)`,
		queue, string(body)).Scan(&msgID)
	if err != nil {
		return fmt.Errorf("pgmq.send %s: %w", queue, err)
	}
	return nil
}
