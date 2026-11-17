package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// RedisPartitions is the number of partition streams per topic. It is part
// of the wire format (docs/spec/events-redis.md): every publisher and
// consumer, in Go and Python, must hash keys to the same partitions.
const RedisPartitions = 8

// Redis stream field names (docs/spec/events-redis.md). Attributes travel
// as fields named redisAttrPrefix + name.
const (
	redisFieldID          = "id"
	redisFieldSource      = "source"
	redisFieldKey         = "key"
	redisFieldTraceparent = "traceparent"
	redisFieldPayload     = "payload"
	redisAttrPrefix       = "attr:"
	redisKeyPrefix        = "skein:events:"
)

// Attributes added to a dead-lettered message, named as Pub/Sub names them
// so a person inspecting dead letters sees the same thing on both.
const (
	AttrDeadLetterSubscription  = "CloudPubSubDeadLetterSourceSubscription"
	AttrDeadLetterDeliveryCount = "CloudPubSubDeadLetterSourceDeliveryCount"
)

// RedisStreamKey is the stream holding one partition of a topic.
func RedisStreamKey(topic string, partition int) string {
	return redisKeyPrefix + "stream:" + topic + ":" + strconv.Itoa(partition)
}

func redisLeaseKey(sub string, p int) string {
	return redisKeyPrefix + "lease:" + sub + ":" + strconv.Itoa(p)
}

func redisAttemptsKey(sub string, p int) string {
	return redisKeyPrefix + "attempts:" + sub + ":" + strconv.Itoa(p)
}

func redisRetryKey(sub string, p int) string {
	return redisKeyPrefix + "retry:" + sub + ":" + strconv.Itoa(p)
}

func redisConsumersKey(sub string) string { return redisKeyPrefix + "consumers:" + sub }

// RedisPartition returns the partition for an envelope: FNV-1a (32-bit) of
// the ordering key, or of the event ID when there is none (unordered
// events spread over every partition), modulo RedisPartitions.
func RedisPartition(orderingKey, id string) int {
	h := fnv.New32a()
	if orderingKey != "" {
		_, _ = h.Write([]byte(orderingKey))
	} else {
		_, _ = h.Write([]byte(id))
	}
	return int(h.Sum32() % RedisPartitions)
}

// RedisConfig configures NewRedis.
type RedisConfig struct {
	// URL names the Redis server (redis:// or rediss://).
	URL string
	// Database overrides the URL's database. Events must be in one
	// database shared by every service, while services keep their caches
	// in different ones, so the default (0) ignores the URL's choice.
	Database int
	// MaxLen caps each partition stream, approximately; default 100 000.
	// An entry trimmed before every subscription acknowledged it is lost
	// to the slow ones (logged when noticed).
	MaxLen int64
	// MaxOutstandingMessages bounds unacknowledged messages per partition,
	// like Pub/Sub's flow control; default 100.
	MaxOutstandingMessages int
	// PollInterval is the longest a worker blocks on an empty partition
	// before it re-checks its retries and its stop signal; default 1 s.
	PollInterval time.Duration
	// ConsumerName identifies this process in consumer groups; default
	// host-pid-random.
	ConsumerName   string
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
	// Client, when set, is used instead of opening URL (tests).
	Client *redis.Client
	Now    func() time.Time
}

// Redis implements Publisher and Subscriber on Redis Streams, with
// Pub/Sub's delivery semantics. See the package documentation, "Redis
// backend", for the design.
type Redis struct {
	client     *redis.Client
	ownsClient bool
	cfg        RedisConfig
	dispatch   dispatcher
	tracer     trace.Tracer

	mu       sync.RWMutex
	topology Topology

	acquisitions atomic.Uint64 // makes every lease token unique
}

// NewRedis connects to Redis. Call Provision before publishing.
func NewRedis(ctx context.Context, cfg RedisConfig) (*Redis, error) {
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = 100_000
	}
	if cfg.MaxOutstandingMessages <= 0 {
		cfg.MaxOutstandingMessages = 100
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = defaultConsumerName()
	}
	client, owns := cfg.Client, false
	if client == nil {
		opts, err := redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("events: redis url: %w", err)
		}
		opts.DB = cfg.Database
		// Each held partition keeps one connection in a blocking read, so
		// the pool is sized for that, apart from the service's cache pool.
		opts.PoolSize = max(opts.PoolSize, 4*RedisPartitions)
		opts.ContextTimeoutEnabled = true
		client, owns = redis.NewClient(opts), true
	}
	if err := client.Ping(ctx).Err(); err != nil {
		if owns {
			_ = client.Close()
		}
		return nil, fmt.Errorf("events: redis ping: %w", err)
	}
	tp := cfg.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &Redis{
		client:     client,
		ownsClient: owns,
		cfg:        cfg,
		dispatch:   newDispatcher(tp, cfg.Logger, "redis"),
		tracer:     tp.Tracer(instrumentation),
	}, nil
}

func defaultConsumerName() string {
	host, _ := os.Hostname()
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// Provision records the topology and creates each subscription's consumer
// group on every partition stream of its topic. It is idempotent, and
// every service runs it at startup, so whichever starts first creates the
// groups. A new group starts at the beginning of an existing stream, so
// events published before the subscriber's first start still reach it.
func (r *Redis) Provision(ctx context.Context, t Topology) error {
	if err := t.Validate(); err != nil {
		return err
	}
	for _, spec := range t.Subscriptions {
		for p := range RedisPartitions {
			err := r.client.XGroupCreateMkStream(ctx, RedisStreamKey(spec.Topic, p), spec.Name, "0").Err()
			if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
				return fmt.Errorf("events: create group %s on %s: %w", spec.Name, spec.Topic, err)
			}
		}
	}
	r.mu.Lock()
	r.topology = t
	r.mu.Unlock()
	return nil
}

// Close closes the client if NewRedis opened it.
func (r *Redis) Close() error {
	if r.ownsClient {
		return r.client.Close()
	}
	return nil
}

// Publish implements Publisher.
func (r *Redis) Publish(ctx context.Context, topic string, e Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	r.mu.RLock()
	known := r.topology.HasTopic(topic)
	r.mu.RUnlock()
	if !known {
		return fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	ctx, span := publishSpan(ctx, r.tracer, "redis", topic, e)
	defer span.End()
	InjectTrace(ctx, &e)
	if err := r.xadd(ctx, topic, RedisPartition(e.OrderingKey, e.ID), toRedisFields(e)); err != nil {
		span.RecordError(err)
		return fmt.Errorf("events: publish to %s: %w", topic, err)
	}
	return nil
}

func (r *Redis) xadd(ctx context.Context, topic string, partition int, fields []any) error {
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: RedisStreamKey(topic, partition), MaxLen: r.cfg.MaxLen, Approx: true, Values: fields,
	}).Err()
}

// toRedisFields is the wire form: fixed fields, then attributes in sorted
// order so the encoding is deterministic.
func toRedisFields(e Envelope) []any {
	fields := []any{redisFieldID, e.ID, redisFieldSource, e.Source}
	if e.OrderingKey != "" {
		fields = append(fields, redisFieldKey, e.OrderingKey)
	}
	if e.Traceparent != "" {
		fields = append(fields, redisFieldTraceparent, e.Traceparent)
	}
	for _, k := range sortedKeys(e.Attributes) {
		fields = append(fields, redisAttrPrefix+k, e.Attributes[k])
	}
	return append(fields, redisFieldPayload, string(e.Payload))
}

// fromRedisFields rebuilds an envelope; unknown fields are ignored so a
// newer publisher can add some.
func fromRedisFields(values map[string]any) (Envelope, error) {
	e := Envelope{Attributes: map[string]string{}}
	for k, raw := range values {
		v, _ := raw.(string)
		switch {
		case k == redisFieldID:
			e.ID = v
		case k == redisFieldSource:
			e.Source = v
		case k == redisFieldKey:
			e.OrderingKey = v
		case k == redisFieldTraceparent:
			e.Traceparent = v
		case k == redisFieldPayload:
			e.Payload = []byte(v)
		case strings.HasPrefix(k, redisAttrPrefix):
			e.Attributes[strings.TrimPrefix(k, redisAttrPrefix)] = v
		}
	}
	if e.ID == "" || e.Source == "" {
		return Envelope{}, fmt.Errorf("%w: entry lacks %s or %s", ErrInvalidEnvelope, redisFieldID, redisFieldSource)
	}
	return e, nil
}

// entryTime is the publish time Redis stamped into the entry ID.
func entryTime(id string) time.Time {
	ms, _, _ := strings.Cut(id, "-")
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// Subscribe implements Subscriber: it leases partitions of the
// subscription, runs one worker per leased partition, and returns nil when
// ctx is cancelled, after the workers finish their current message and the
// leases are released.
func (r *Redis) Subscribe(ctx context.Context, subscription string, h Handler) error {
	r.mu.RLock()
	spec, ok := r.topology.Subscription(subscription)
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSubscription, subscription)
	}
	s := &redisSubscription{
		r: r, spec: spec, handler: h,
		consumer: r.cfg.ConsumerName + "/" + strconv.FormatUint(r.acquisitions.Add(1), 10),
		workers:  map[int]*redisWorker{},
	}
	return s.run(ctx)
}

var (
	renewLease = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) end return 0`)
	freeLease  = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) end return 0`)
)

// redisSubscription is one Subscribe call: a consumer that balances
// partition leases with the subscription's other consumers.
type redisSubscription struct {
	r        *Redis
	spec     SubscriptionSpec
	handler  Handler
	consumer string

	workers map[int]*redisWorker
	exited  sync.WaitGroup
}

func (s *redisSubscription) run(ctx context.Context) error {
	tick := time.NewTicker(max(s.spec.AckDeadline/3, 10*time.Millisecond))
	defer tick.Stop()
	defer s.shutdown()
	for {
		if err := s.balance(ctx); err != nil && ctx.Err() == nil {
			s.r.dispatch.logger.WarnContext(ctx, "event partition balancing failed; retrying",
				slog.String("subscription", s.spec.Name), slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// balance heartbeats, renews held leases, gives up partitions beyond this
// consumer's fair share (ceil(partitions / live consumers)) and acquires
// free ones up to it.
func (s *redisSubscription) balance(ctx context.Context) error {
	r, ttl := s.r, s.spec.AckDeadline
	now := r.cfg.Now()
	key := redisConsumersKey(s.spec.Name)
	pipe := r.client.TxPipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMilli()), Member: s.consumer})
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-ttl).UnixMilli(), 10))
	live := pipe.ZCard(ctx, key)
	pipe.PExpire(ctx, key, 2*ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	share := (RedisPartitions + int(max(live.Val(), 1)) - 1) / int(max(live.Val(), 1))

	for p, w := range s.workers {
		held, err := renewLease.Run(ctx, r.client, []string{redisLeaseKey(s.spec.Name, p)}, w.token, ttl.Milliseconds()).Int()
		if err != nil {
			return err
		}
		if held == 0 {
			r.dispatch.logger.WarnContext(ctx, "lost an event partition lease",
				slog.String("subscription", s.spec.Name), slog.Int("partition", p))
			s.stopWorker(p)
		}
	}
	for p := RedisPartitions - 1; p >= 0 && len(s.workers) > share; p-- {
		if _, ok := s.workers[p]; ok {
			s.stopWorker(p)
		}
	}
	// Start the scan at a consumer-specific offset so consumers starting
	// together do not all race for partition 0 first.
	offset := RedisPartition(s.consumer, "")
	for i := 0; i < RedisPartitions && len(s.workers) < share; i++ {
		p := (offset + i) % RedisPartitions
		if _, ok := s.workers[p]; ok {
			continue
		}
		token := s.consumer + "#" + strconv.FormatUint(r.acquisitions.Add(1), 10)
		got, err := r.client.SetNX(ctx, redisLeaseKey(s.spec.Name, p), token, ttl).Result()
		if err != nil {
			return err
		}
		if !got {
			continue
		}
		if err := s.claimAll(ctx, p); err != nil {
			_, _ = freeLease.Run(context.WithoutCancel(ctx), r.client, []string{redisLeaseKey(s.spec.Name, p)}, token).Result()
			return err
		}
		s.startWorker(ctx, p, token)
	}
	return nil
}

// claimAll moves every pending entry of the partition to this consumer.
// Holding the lease makes that safe: the previous holder either released
// it (and finished its work) or stopped renewing it an ack deadline ago,
// which is the visibility timeout after which its deliveries are retried.
// It runs on acquisition and again at the start of every pass: a former
// holder that has not yet noticed its lease lapsed can still read a new
// entry into its own pending list and then stop, and without the repeat
// that entry would wait for the partition to change hands again. While a
// single lease holds, every pending entry is already this consumer's, so
// the repeat claims nothing.
func (s *redisSubscription) claimAll(ctx context.Context, p int) error {
	start := "0-0"
	for {
		_, next, err := s.r.client.XAutoClaimJustID(ctx, &redis.XAutoClaimArgs{
			Stream: RedisStreamKey(s.spec.Topic, p), Group: s.spec.Name, Consumer: s.consumer,
			MinIdle: 0, Start: start, Count: 1000,
		}).Result()
		if err != nil {
			return err
		}
		if next == "0-0" || next == "" {
			return nil
		}
		start = next
	}
}

func (s *redisSubscription) startWorker(ctx context.Context, p int, token string) {
	w := &redisWorker{s: s, partition: p, token: token, stop: make(chan struct{})}
	s.workers[p] = w
	s.exited.Add(1)
	go func() {
		defer s.exited.Done()
		w.run(ctx)
	}()
}

func (s *redisSubscription) stopWorker(p int) {
	close(s.workers[p].stop)
	delete(s.workers, p)
}

func (s *redisSubscription) shutdown() {
	for p := range s.workers {
		s.stopWorker(p)
	}
	s.exited.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.r.client.ZRem(ctx, redisConsumersKey(s.spec.Name), s.consumer).Err()
}

// redisWorker delivers one leased partition, one message at a time, in
// entry order.
type redisWorker struct {
	s         *redisSubscription
	partition int
	token     string
	stop      chan struct{}
}

func (w *redisWorker) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

func (w *redisWorker) run(ctx context.Context) {
	defer func() {
		// Released even when ctx is done, so another consumer can take the
		// partition at once instead of after the lease expires.
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = freeLease.Run(release, w.s.r.client, []string{redisLeaseKey(w.s.spec.Name, w.partition)}, w.token).Result()
	}()
	for ctx.Err() == nil && !w.stopped() {
		wait, err := w.pass(ctx)
		if err != nil && ctx.Err() == nil {
			w.s.r.dispatch.logger.WarnContext(ctx, "event partition pass failed; retrying",
				slog.String("subscription", w.s.spec.Name), slog.Int("partition", w.partition), slog.String("error", err.Error()))
			wait = w.s.r.cfg.PollInterval
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
			case <-w.stop:
			case <-time.After(wait):
			}
		}
	}
}

// passState is what one pass knows: retry times read at its start, keys
// held back so far, and the earliest moment something becomes due.
type passState struct {
	retries  map[string]int64 // entry ID -> unix ms before which it is not redelivered
	blocked  map[string]bool  // ordering keys with an earlier undelivered message
	nextWake time.Time
	resolved int // acked or dead-lettered this pass
	waiting  int // left pending this pass
}

// pass adopts any pending entry another consumer left behind (claimAll),
// then delivers the partition's pending entries, then new ones, in entry
// ID order (pending IDs are all below the group's last-delivered ID, so
// pending-then-new is ID order). It returns how long to sleep before the
// next pass; a blocking read for new entries usually did the waiting
// already.
func (w *redisWorker) pass(ctx context.Context) (time.Duration, error) {
	r, spec := w.s.r, w.s.spec
	stream := RedisStreamKey(spec.Topic, w.partition)
	if err := w.s.claimAll(ctx, w.partition); err != nil {
		return 0, err
	}
	raw, err := r.client.HGetAll(ctx, redisRetryKey(spec.Name, w.partition)).Result()
	if err != nil {
		return 0, err
	}
	st := &passState{retries: make(map[string]int64, len(raw)), blocked: map[string]bool{},
		nextWake: r.cfg.Now().Add(r.cfg.PollInterval)}
	for id, at := range raw {
		n, _ := strconv.ParseInt(at, 10, 64)
		st.retries[id] = n
	}

	const page = 100
	for start := "0"; ; {
		entries, err := w.read(ctx, stream, start, page, -1)
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			start = e.ID
			if err := w.deliver(ctx, e, st); err != nil {
				return 0, err
			}
			if ctx.Err() != nil || w.stopped() {
				return 0, nil
			}
		}
		if len(entries) < page {
			break
		}
	}

	room := r.cfg.MaxOutstandingMessages - st.waiting
	if room <= 0 {
		return max(time.Until(st.nextWake), time.Millisecond), nil
	}
	block := time.Duration(-1) // no BLOCK: go-redis sends BLOCK 0 (forever) for zero
	if st.resolved == 0 {
		block = max(st.nextWake.Sub(r.cfg.Now()), time.Millisecond)
	}
	entries, err := w.read(ctx, stream, ">", room, block)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := w.deliver(ctx, e, st); err != nil {
			return 0, err
		}
		if ctx.Err() != nil || w.stopped() {
			break
		}
	}
	return 0, nil
}

func (w *redisWorker) read(ctx context.Context, stream, start string, count int, block time.Duration) ([]redis.XMessage, error) {
	res, err := w.s.r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: w.s.spec.Name, Consumer: w.s.consumer, Streams: []string{stream, start}, Count: int64(count), Block: block,
	}).Result()
	if errors.Is(err, redis.Nil) || (err != nil && ctx.Err() != nil) {
		return nil, nil
	}
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return res[0].Messages, nil
}

// deliver hands one entry to the handler if its turn has come, and records
// the outcome: ack, dead letter, or a retry time that also holds back its
// key for the rest of the pass.
func (w *redisWorker) deliver(ctx context.Context, e redis.XMessage, st *passState) error {
	r, spec := w.s.r, w.s.spec
	if len(e.Values) == 0 {
		r.dispatch.logger.WarnContext(ctx, "pending event was trimmed from its stream before delivery; dropping",
			slog.String("subscription", spec.Name), slog.String("entry_id", e.ID))
		st.resolved++
		return w.finish(ctx, e.ID)
	}
	key, _ := e.Values[redisFieldKey].(string)
	if key != "" && st.blocked[key] {
		st.waiting++
		return nil
	}
	if at, ok := st.retries[e.ID]; ok && at > r.cfg.Now().UnixMilli() {
		w.holdBack(key, time.UnixMilli(at), st)
		return nil
	}
	attempt, err := r.client.HIncrBy(ctx, redisAttemptsKey(spec.Name, w.partition), e.ID, 1).Result()
	if err != nil {
		return err
	}
	acked := false
	env, decodeErr := fromRedisFields(e.Values)
	if decodeErr != nil {
		r.dispatch.logger.ErrorContext(ctx, "malformed event; nacking toward the dead-letter topic",
			slog.String("subscription", spec.Name), slog.String("entry_id", e.ID), slog.String("error", decodeErr.Error()))
	} else {
		msg := Message{Envelope: env, PublishTime: entryTime(e.ID), DeliveryAttempt: int(attempt)}
		acked = r.dispatch.dispatch(ctx, spec.Name, w.s.handler, msg)
	}
	switch {
	case acked:
		st.resolved++
		return w.finish(ctx, e.ID)
	case spec.DeadLetterTopic != "" && int(attempt) >= spec.MaxDeliveryAttempts:
		if err := w.deadLetter(ctx, e, int(attempt)); err != nil {
			return err
		}
		st.resolved++
		return w.finish(ctx, e.ID)
	default:
		at := r.cfg.Now().Add(spec.Backoff(int(attempt)))
		// Rounded up to the next millisecond: the retry check compares
		// against the current time in whole milliseconds, so a truncated
		// deadline would let the redelivery run up to 1 ms early.
		if err := r.client.HSet(ctx, redisRetryKey(spec.Name, w.partition), e.ID, ceilUnixMilli(at)).Err(); err != nil {
			return err
		}
		w.holdBack(key, at, st)
		return nil
	}
}

func ceilUnixMilli(t time.Time) int64 {
	ms := t.UnixMilli()
	if time.UnixMilli(ms).Before(t) {
		ms++
	}
	return ms
}

func (w *redisWorker) holdBack(key string, until time.Time, st *passState) {
	st.waiting++
	if key != "" {
		st.blocked[key] = true
	}
	if until.Before(st.nextWake) {
		st.nextWake = until
	}
}

// finish acknowledges an entry and forgets its delivery state.
func (w *redisWorker) finish(ctx context.Context, id string) error {
	spec := w.s.spec
	ctx = context.WithoutCancel(ctx) // an ack after a completed handler must not be lost to shutdown
	pipe := w.s.r.client.TxPipeline()
	pipe.XAck(ctx, RedisStreamKey(spec.Topic, w.partition), spec.Name, id)
	pipe.HDel(ctx, redisAttemptsKey(spec.Name, w.partition), id)
	pipe.HDel(ctx, redisRetryKey(spec.Name, w.partition), id)
	_, err := pipe.Exec(ctx)
	return err
}

// deadLetter copies the entry's fields unchanged (so even a malformed one
// can be inspected) to the dead-letter topic, adding Pub/Sub's attributes.
func (w *redisWorker) deadLetter(ctx context.Context, e redis.XMessage, attempts int) error {
	spec := w.s.spec
	fields := make([]any, 0, 2*len(e.Values)+4)
	for _, k := range sortedKeys(e.Values) {
		fields = append(fields, k, e.Values[k])
	}
	fields = append(fields,
		redisAttrPrefix+AttrDeadLetterSubscription, spec.Name,
		redisAttrPrefix+AttrDeadLetterDeliveryCount, strconv.Itoa(attempts))
	key, _ := e.Values[redisFieldKey].(string)
	id, _ := e.Values[redisFieldID].(string)
	if id == "" {
		id = e.ID
	}
	w.s.r.dispatch.logger.ErrorContext(ctx, "event dead-lettered",
		slog.String("subscription", spec.Name), slog.String("event_id", id), slog.Int("delivery_attempts", attempts))
	return w.s.r.xadd(context.WithoutCancel(ctx), spec.DeadLetterTopic, RedisPartition(key, id), fields)
}

// CheckInvariants verifies, for every partition of the subscription, that
// delivery state refers only to pending entries (attempts and retry times
// are forgotten on ack), that every retry time belongs to an entry with
// attempts, and that no pending entry has used up its attempts when the
// subscription dead-letters.
func (r *Redis) CheckInvariants(ctx context.Context, subscription string) error {
	r.mu.RLock()
	spec, ok := r.topology.Subscription(subscription)
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSubscription, subscription)
	}
	for p := range RedisPartitions {
		pending, err := r.client.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: RedisStreamKey(spec.Topic, p), Group: spec.Name, Start: "-", End: "+", Count: 1 << 20,
		}).Result()
		if err != nil {
			return err
		}
		inPEL := make(map[string]bool, len(pending))
		for _, pe := range pending {
			inPEL[pe.ID] = true
		}
		attempts, err := r.client.HGetAll(ctx, redisAttemptsKey(spec.Name, p)).Result()
		if err != nil {
			return err
		}
		retries, err := r.client.HKeys(ctx, redisRetryKey(spec.Name, p)).Result()
		if err != nil {
			return err
		}
		for id, n := range attempts {
			count, _ := strconv.Atoi(n)
			if !inPEL[id] {
				return fmt.Errorf("events: invariant: %s/%d keeps attempts for acknowledged entry %s", subscription, p, id)
			}
			if spec.DeadLetterTopic != "" && count >= spec.MaxDeliveryAttempts {
				return fmt.Errorf("events: invariant: %s/%d keeps %s after %d attempts", subscription, p, id, count)
			}
		}
		for _, id := range retries {
			if _, ok := attempts[id]; !ok {
				return fmt.Errorf("events: invariant: %s/%d has a retry time for %s without attempts", subscription, p, id)
			}
		}
	}
	return nil
}
