package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// buildSubBuffer is the per-subscriber channel buffer. A slow WebSocket
// consumer that fills the buffer drops live events; it recovers the full
// build state from the DB log on reconnect.
const buildSubBuffer = 256

// buildBrokerReconnect is the backoff before re-subscribing to Redis after a
// subscription error.
const buildBrokerReconnect = 2 * time.Second

// BuildBroker fans build events out from per-build Redis pub/sub channels to
// in-process WebSocket subscribers. A Redis subscription is started lazily for
// a build when its first client connects and torn down when the last leaves.
//
// The build worker publishes via publishBuildEvent (Redis only); the broker is
// purely the read/fan-out side. Decoupling through Redis means the worker and
// the WebSocket handler need not run in the same process.
type BuildBroker struct {
	rdb    *redis.Client
	mu     sync.Mutex
	builds map[string]*buildFanout
}

type buildFanout struct {
	subs   map[chan BuildStreamEvent]struct{}
	cancel context.CancelFunc
}

// NewBuildBroker creates a broker reading from the given Redis client.
func NewBuildBroker(rdb *redis.Client) *BuildBroker {
	return &BuildBroker{rdb: rdb, builds: make(map[string]*buildFanout)}
}

// Subscribe registers an in-process subscriber for buildID's event stream and
// returns the receive channel plus a release function. The first subscriber
// for a build starts its Redis subscription; the last to release stops it.
// The release function is idempotent and closes the channel.
func (b *BuildBroker) Subscribe(buildID string) (<-chan BuildStreamEvent, func()) {
	ch := make(chan BuildStreamEvent, buildSubBuffer)

	b.mu.Lock()
	fan, ok := b.builds[buildID]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		fan = &buildFanout{subs: make(map[chan BuildStreamEvent]struct{}), cancel: cancel}
		b.builds[buildID] = fan
		go b.run(ctx, buildID)
	}
	fan.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			fan, ok := b.builds[buildID]
			if !ok {
				return
			}
			if _, present := fan.subs[ch]; !present {
				return
			}
			delete(fan.subs, ch)
			close(ch)
			if len(fan.subs) == 0 {
				fan.cancel()
				delete(b.builds, buildID)
			}
		})
	}
	return ch, release
}

// run keeps a Redis subscription alive for buildID, reconnecting on error,
// until the fanout's context is cancelled (last subscriber left).
func (b *BuildBroker) run(ctx context.Context, buildID string) {
	for ctx.Err() == nil {
		b.subscribeOnce(ctx, buildID)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(buildBrokerReconnect):
		}
	}
}

func (b *BuildBroker) subscribeOnce(ctx context.Context, buildID string) {
	sub := b.rdb.Subscribe(ctx, buildStreamChannel(buildID))
	defer sub.Close()

	msgCh := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			var ev BuildStreamEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				slog.Warn("build broker: bad event payload", "build_id", buildID, "error", err)
				continue
			}
			b.dispatch(buildID, ev)
		}
	}
}

// dispatch fans one event to every in-process subscriber. The send is
// non-blocking; a full subscriber buffer drops the event. The mutex is held
// for the whole dispatch so a concurrent release cannot close a channel
// mid-send.
func (b *BuildBroker) dispatch(buildID string, ev BuildStreamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fan, ok := b.builds[buildID]
	if !ok {
		return
	}
	for ch := range fan.subs {
		select {
		case ch <- ev:
		default:
			slog.Debug("build broker: dropped event for slow consumer", "build_id", buildID)
		}
	}
}
