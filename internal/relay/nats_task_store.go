package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/codewiresh/codewire/internal/store"
)

const (
	defaultTaskSubjectRoot  = "tasks"
	defaultTaskEventsStream = "TASK_EVENTS"
	defaultTaskLatestBucket = "TASK_LATEST"
)

type taskEventPublisher interface {
	PublishTaskEvent(ctx context.Context, subject, eventID string, payload []byte) (uint64, error)
}

type taskLatestWriter interface {
	PutLatestTask(ctx context.Context, key string, payload []byte) error
}

type jetStreamTaskPublisher struct {
	js jetstream.JetStream
}

func (p jetStreamTaskPublisher) PublishTaskEvent(ctx context.Context, subject, eventID string, payload []byte) (uint64, error) {
	ack, err := p.js.Publish(ctx, subject, payload, jetstream.WithMsgID(eventID))
	if err != nil {
		return 0, err
	}
	return ack.Sequence, nil
}

type jetStreamTaskLatestWriter struct {
	kv jetstream.KeyValue
}

func (w jetStreamTaskLatestWriter) PutLatestTask(ctx context.Context, key string, payload []byte) error {
	_, err := w.kv.Put(ctx, key, payload)
	return err
}

// NATSTaskStore persists task reports into JetStream and maintains latest
// session task state in a KV bucket.
type NATSTaskStore struct {
	nc               *nats.Conn
	js               jetstream.JetStream
	kv               jetstream.KeyValue
	publisher        taskEventPublisher
	latest           taskLatestWriter
	subjectRoot      string
	eventsStreamName string
	latestBucketName string
}

func NewNATSTaskStore(ctx context.Context, cfg RelayConfig) (*NATSTaskStore, error) {
	natsURL := strings.TrimSpace(cfg.NATSURL)
	if natsURL == "" {
		return nil, fmt.Errorf("nats url required")
	}

	opts := []nats.Option{
		nats.Name("codewire-relay-task-store"),
	}
	if creds := strings.TrimSpace(cfg.NATSCredsFile); creds != "" {
		opts = append(opts, nats.UserCredentials(creds))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	kv, err := ensureTaskInfra(ctx, js, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}

	return &NATSTaskStore{
		nc:               nc,
		js:               js,
		kv:               kv,
		publisher:        jetStreamTaskPublisher{js: js},
		latest:           jetStreamTaskLatestWriter{kv: kv},
		subjectRoot:      taskSubjectRoot(cfg),
		eventsStreamName: taskEventsStreamName(cfg),
		latestBucketName: taskLatestBucketName(cfg),
	}, nil
}

func (s *NATSTaskStore) Close() error {
	if s == nil || s.nc == nil {
		return nil
	}
	s.nc.Close()
	return nil
}

func (s *NATSTaskStore) PublishNodeTaskReport(ctx context.Context, node store.NodeRecord, msg TaskReportMessage) (*LatestTaskValue, error) {
	if s == nil {
		return nil, fmt.Errorf("task store is nil")
	}
	networkID := strings.TrimSpace(node.NetworkID)
	if err := validateNetworkID(networkID); err != nil {
		return nil, fmt.Errorf("invalid network id: %w", err)
	}
	nodeName := strings.TrimSpace(node.Name)
	if nodeName == "" {
		return nil, fmt.Errorf("node name required")
	}

	event, err := taskEventFromNodeReport(node, msg)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal task event: %w", err)
	}

	subject := subjectForTask(s.subjectRoot, event.NetworkID, event.NodeName, event.SessionID)
	streamSeq, err := s.publisher.PublishTaskEvent(ctx, subject, event.EventID, payload)
	if err != nil {
		return nil, fmt.Errorf("publish task event: %w", err)
	}

	latest := LatestTaskValue{
		EventID:     event.EventID,
		StreamSeq:   streamSeq,
		NetworkID:   event.NetworkID,
		NodeName:    event.NodeName,
		SessionID:   event.SessionID,
		SessionName: event.SessionName,
		Summary:     event.Summary,
		State:       event.State,
		Timestamp:   event.Timestamp,
	}
	latestPayload, err := json.Marshal(latest)
	if err != nil {
		return nil, fmt.Errorf("marshal latest task value: %w", err)
	}
	if err := s.latest.PutLatestTask(ctx, kvKeyForTask(event.NetworkID, event.NodeName, event.SessionID), latestPayload); err != nil {
		return nil, fmt.Errorf("update latest task value: %w", err)
	}

	return &latest, nil
}

func (s *NATSTaskStore) ListLatestTasks(ctx context.Context, filter TaskFilter) ([]LatestTaskValue, error) {
	if s == nil || s.kv == nil {
		return nil, nil
	}
	filter = normalizeTaskFilter(filter)
	if filter.NetworkID == "" {
		return nil, fmt.Errorf("network_id required")
	}

	lister, err := s.kv.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list latest task keys: %w", err)
	}
	defer lister.Stop()

	var out []LatestTaskValue
	for key := range lister.Keys() {
		entry, err := s.kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, jetstream.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("get latest task %q: %w", key, err)
		}
		var value LatestTaskValue
		if err := json.Unmarshal(entry.Value(), &value); err != nil {
			return nil, fmt.Errorf("decode latest task %q: %w", key, err)
		}
		if !matchesLatestTaskFilter(value, filter) {
			continue
		}
		out = append(out, value)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeName != out[j].NodeName {
			return out[i].NodeName < out[j].NodeName
		}
		if out[i].SessionName != out[j].SessionName {
			return out[i].SessionName < out[j].SessionName
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

func (s *NATSTaskStore) WatchTasks(ctx context.Context, filter TaskFilter, afterSeq uint64) (TaskWatcher, error) {
	if s == nil || s.js == nil {
		return closedTaskWatcher{}, nil
	}
	filter = normalizeTaskFilter(filter)
	if filter.NetworkID == "" {
		return nil, fmt.Errorf("network_id required")
	}

	cfg := jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subjectFilter(s.subjectRoot, filter)},
	}
	if afterSeq > 0 {
		cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		cfg.OptStartSeq = afterSeq + 1
	} else {
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}

	consumer, err := s.js.OrderedConsumer(ctx, s.eventsStreamName, cfg)
	if err != nil {
		return nil, fmt.Errorf("create ordered task watcher: %w", err)
	}
	messages, err := consumer.Messages()
	if err != nil {
		return nil, fmt.Errorf("create task message iterator: %w", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	events := make(chan TaskWatchEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		for {
			msg, err := messages.Next(jetstream.NextContext(watchCtx))
			if err != nil {
				if watchCtx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
					return
				}
				return
			}
			meta, err := msg.Metadata()
			if err != nil {
				return
			}
			var event TaskEvent
			if err := json.Unmarshal(msg.Data(), &event); err != nil {
				continue
			}
			if !matchesTaskEventFilter(event, filter) {
				continue
			}
			select {
			case events <- TaskWatchEvent{Seq: meta.Sequence.Stream, Event: event}:
			case <-watchCtx.Done():
				return
			}
		}
	}()

	return &natsTaskWatcher{
		events:  events,
		done:    done,
		cancel:  cancel,
		stopper: messages,
	}, nil
}

func (s *NATSTaskStore) EarliestSeq(ctx context.Context, networkID string) (uint64, error) {
	if s == nil || s.js == nil {
		return 0, nil
	}
	stream, err := s.js.Stream(ctx, s.eventsStreamName)
	if err != nil {
		return 0, fmt.Errorf("load task stream: %w", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("load task stream info: %w", err)
	}
	return info.State.FirstSeq, nil
}

func ensureTaskInfra(ctx context.Context, js jetstream.JetStream, cfg RelayConfig) (jetstream.KeyValue, error) {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       taskEventsStreamName(cfg),
		Subjects:   []string{taskSubjectRoot(cfg) + ".>"},
		Retention:  jetstream.LimitsPolicy,
		Storage:    jetstream.FileStorage,
		Replicas:   1,
		Duplicates: 2 * time.Minute,
	}); err != nil {
		return nil, fmt.Errorf("ensure task events stream: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   taskLatestBucketName(cfg),
		History:  1,
		Storage:  jetstream.FileStorage,
		Replicas: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure task latest bucket: %w", err)
	}
	return kv, nil
}

func subjectForTask(root, networkID, nodeName string, sessionID uint32) string {
	return strings.TrimSpace(root) + "." + strings.TrimSpace(networkID) + "." + strings.TrimSpace(nodeName) + fmt.Sprintf(".s%d", sessionID)
}

func kvKeyForTask(networkID, nodeName string, sessionID uint32) string {
	return strings.TrimSpace(networkID) + "." + strings.TrimSpace(nodeName) + fmt.Sprintf(".s%d", sessionID)
}

func subjectFilter(root string, filter TaskFilter) string {
	root = strings.TrimSpace(root)
	filter = normalizeTaskFilter(filter)
	switch {
	case filter.SessionID != nil:
		nodeName := filter.NodeName
		if nodeName == "" {
			nodeName = "*"
		}
		return subjectForTask(root, filter.NetworkID, nodeName, *filter.SessionID)
	case filter.NodeName != "":
		return root + "." + filter.NetworkID + "." + filter.NodeName + ".>"
	default:
		return root + "." + filter.NetworkID + ".>"
	}
}

func taskSubjectRoot(cfg RelayConfig) string {
	if root := strings.TrimSpace(cfg.NATSSubjectRoot); root != "" {
		return root
	}
	return defaultTaskSubjectRoot
}

func taskEventsStreamName(cfg RelayConfig) string {
	if name := strings.TrimSpace(cfg.TaskEventsStream); name != "" {
		return name
	}
	return defaultTaskEventsStream
}

func taskLatestBucketName(cfg RelayConfig) string {
	if name := strings.TrimSpace(cfg.TaskLatestBucket); name != "" {
		return name
	}
	return defaultTaskLatestBucket
}

func taskEventFromNodeReport(node store.NodeRecord, msg TaskReportMessage) (TaskEvent, error) {
	eventID := strings.TrimSpace(msg.EventID)
	if eventID == "" {
		return TaskEvent{}, fmt.Errorf("event_id required")
	}
	if msg.SessionID == 0 {
		return TaskEvent{}, fmt.Errorf("session_id required")
	}
	summary := strings.TrimSpace(msg.Summary)
	if summary == "" {
		return TaskEvent{}, fmt.Errorf("summary required")
	}
	state := strings.TrimSpace(msg.State)
	if !isValidTaskState(state) {
		return TaskEvent{}, fmt.Errorf("invalid task state %q", msg.State)
	}
	timestamp, err := normalizeTaskTimestamp(msg.Timestamp)
	if err != nil {
		return TaskEvent{}, err
	}

	return TaskEvent{
		EventID:     eventID,
		NetworkID:   strings.TrimSpace(node.NetworkID),
		NodeName:    strings.TrimSpace(node.Name),
		SessionID:   msg.SessionID,
		SessionName: strings.TrimSpace(msg.SessionName),
		Summary:     summary,
		State:       state,
		Timestamp:   timestamp,
	}, nil
}

func normalizeTaskTimestamp(raw string) (string, error) {
	ts := strings.TrimSpace(raw)
	if ts == "" {
		return "", fmt.Errorf("timestamp required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "", fmt.Errorf("invalid timestamp %q", raw)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func isValidTaskState(state string) bool {
	switch strings.TrimSpace(state) {
	case "working", "complete", "blocked", "failed":
		return true
	default:
		return false
	}
}

func normalizeTaskFilter(filter TaskFilter) TaskFilter {
	filter.NetworkID = strings.TrimSpace(filter.NetworkID)
	filter.NodeName = strings.TrimSpace(filter.NodeName)
	filter.State = strings.TrimSpace(filter.State)
	return filter
}

func matchesLatestTaskFilter(value LatestTaskValue, filter TaskFilter) bool {
	filter = normalizeTaskFilter(filter)
	if filter.NetworkID != "" && value.NetworkID != filter.NetworkID {
		return false
	}
	if filter.NodeName != "" && value.NodeName != filter.NodeName {
		return false
	}
	if filter.SessionID != nil && value.SessionID != *filter.SessionID {
		return false
	}
	if filter.State != "" && value.State != filter.State {
		return false
	}
	return true
}

func matchesTaskEventFilter(event TaskEvent, filter TaskFilter) bool {
	filter = normalizeTaskFilter(filter)
	if filter.NetworkID != "" && event.NetworkID != filter.NetworkID {
		return false
	}
	if filter.NodeName != "" && event.NodeName != filter.NodeName {
		return false
	}
	if filter.SessionID != nil && event.SessionID != *filter.SessionID {
		return false
	}
	if filter.State != "" && event.State != filter.State {
		return false
	}
	return true
}

type natsTaskWatcher struct {
	events  chan TaskWatchEvent
	done    chan struct{}
	cancel  context.CancelFunc
	stopper interface{ Stop() }
}

func (w *natsTaskWatcher) Events() <-chan TaskWatchEvent {
	return w.events
}

func (w *natsTaskWatcher) Close() error {
	if w == nil {
		return nil
	}
	if w.cancel != nil {
		w.cancel()
	}
	if w.stopper != nil {
		w.stopper.Stop()
	}
	if w.done != nil {
		<-w.done
	}
	return nil
}
