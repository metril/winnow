package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"winnow/internal/db"
)

// broker fans Postgres NOTIFYs out to connected SSE clients.
type broker struct {
	mu      sync.Mutex
	clients map[chan []byte]bool
}

func newBroker() *broker { return &broker{clients: map[chan []byte]bool{}} }

func (b *broker) add() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()
	return ch
}

func (b *broker) remove(ch chan []byte) {
	b.mu.Lock()
	if b.clients[ch] {
		delete(b.clients, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *broker) publish(msg []byte) {
	b.mu.Lock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default: // drop for slow clients
		}
	}
	b.mu.Unlock()
}

// run LISTENs for winnow notifications and broadcasts SSE events.
func (b *broker) run(ctx context.Context, d *db.DB) {
	for ctx.Err() == nil {
		conn, err := d.Pool().Acquire(ctx)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		_, _ = conn.Exec(ctx, "LISTEN winnow")
		_, _ = conn.Exec(ctx, "LISTEN winnow_ref")
		_, _ = conn.Exec(ctx, "LISTEN winnow_config")
		for ctx.Err() == nil {
			n, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				break
			}
			var ev string
			switch n.Channel {
			case "winnow":
				ev = fmt.Sprintf(`{"type":"reading","endpoint_id":%s}`, n.Payload)
			case "winnow_ref":
				ev = fmt.Sprintf(`{"type":"reference","power":%s}`, n.Payload)
			case "winnow_config":
				ev = `{"type":"config"}`
			default:
				continue
			}
			b.publish([]byte(ev))
		}
		conn.Release()
		log.Printf("[api] notification listener reconnecting")
		time.Sleep(time.Second)
	}
}
