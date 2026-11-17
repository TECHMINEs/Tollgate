// Package broker wires Tollgate to a Kafka-API broker (Redpanda locally, any Kafka in
// production) with franz-go.
//
// Consumers commit offsets only after the database transaction for a record has
// committed. A crash in between redelivers the record, and the inbox turns the replay
// into a no-op: at-least-once delivery, exactly-once effects.
package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/sim"
)

// TopicSpec is a topic Tollgate owns.
type TopicSpec struct {
	Name       string
	Partitions int32
	Configs    map[string]*string
}

func ptr(s string) *string { return &s }

// Topics lists every topic and its settings. Inbound freight documents are kept for a
// week so a consumer group can be rewound and replayed; the DLQ for a month.
var Topics = []TopicSpec{
	{events.TopicFreight, sim.Partitions, map[string]*string{"retention.ms": ptr("604800000"), "cleanup.policy": ptr("delete")}},
	{events.TopicVerdicts, sim.Partitions, map[string]*string{"retention.ms": ptr("604800000")}},
	{events.TopicJournal, sim.Partitions, map[string]*string{"retention.ms": ptr("604800000")}},
	{events.TopicDLQ, 1, map[string]*string{"retention.ms": ptr("2592000000")}},
}

// Brokers splits a comma-separated seed list.
func Brokers(s string) []string {
	var out []string
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// EnsureTopics creates any missing topics. Existing topics are left as they are.
func EnsureTopics(ctx context.Context, cl *kgo.Client, replication int16) error {
	adm := kadm.NewClient(cl)
	var errs []error
	for _, t := range Topics {
		res, err := adm.CreateTopic(ctx, t.Partitions, replication, t.Configs, t.Name)
		if err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, err))
		} else if res.Err != nil && !errors.Is(res.Err, kerr.TopicAlreadyExists) {
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, res.Err))
		}
	}
	return errors.Join(errs...)
}

// Record builds a keyed record for an envelope.
func Record(topic string, env events.Envelope, value []byte) *kgo.Record {
	return &kgo.Record{
		Topic: topic, Key: []byte(env.Key()), Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "tenant_id", Value: []byte(env.TenantID)},
			{Key: "type", Value: []byte(env.Type)},
			{Key: "event_id", Value: []byte(env.EventID)},
		},
	}
}

// Handler processes one record. Return a permanent error to dead-letter it; any other
// error is retried with backoff while the partition waits.
type Handler func(ctx context.Context, r *kgo.Record) error

// Consumer runs a consumer group over the inbound topic.
type Consumer struct {
	Client      *kgo.Client
	Handle      Handler
	IsPermanent func(error) bool
	Log         *slog.Logger
	MaxBackoff  time.Duration
}

// NewConsumerClient builds a group client with manual commits. BlockRebalanceOnPoll
// keeps partitions from moving while a polled batch is still being processed.
func NewConsumerClient(brokers []string, group string, extra ...kgo.Opt) (*kgo.Client, error) {
	opts := append([]kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(events.TopicFreight),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	}, extra...)
	return kgo.NewClient(opts...)
}

// Run polls until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 30 * time.Second
	}
	for {
		fetches := c.Client.PollRecords(ctx, 500)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return ctx.Err()
		}
		fetches.EachError(func(t string, p int32, err error) {
			c.Log.Warn("fetch error", "topic", t, "partition", p, "err", err)
		})
		var processErr error
		fetches.EachPartition(func(ftp kgo.FetchTopicPartition) {
			if processErr != nil {
				return
			}
			for _, r := range ftp.Records {
				if err := c.process(ctx, r); err != nil {
					processErr = err
					return
				}
			}
		})
		if processErr != nil {
			c.Client.AllowRebalance()
			return processErr
		}
		if err := c.Client.CommitUncommittedOffsets(ctx); err != nil {
			c.Log.Warn("commit failed; records will be redelivered and dropped by the inbox", "err", err)
		}
		c.Client.AllowRebalance()
	}
}

// process retries transient failures in place, so offsets on this partition never move
// past an unprocessed record.
func (c *Consumer) process(ctx context.Context, r *kgo.Record) error {
	backoff := 200 * time.Millisecond
	for {
		err := c.Handle(ctx, r)
		if err == nil {
			return nil
		}
		if c.IsPermanent(err) {
			c.Log.Warn("dead-lettering record", "partition", r.Partition, "offset", r.Offset, "err", err)
			return c.deadLetter(ctx, r, err)
		}
		c.Log.Error("transient failure; retrying", "partition", r.Partition, "offset", r.Offset, "backoff", backoff, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.MaxBackoff)
	}
}

func (c *Consumer) deadLetter(ctx context.Context, r *kgo.Record, cause error) error {
	dlq := &kgo.Record{Topic: events.TopicDLQ, Key: r.Key, Value: r.Value, Headers: append(r.Headers,
		kgo.RecordHeader{Key: "dlq_reason", Value: []byte(cause.Error())},
		kgo.RecordHeader{Key: "dlq_source", Value: []byte(fmt.Sprintf("%s/%d/%d", r.Topic, r.Partition, r.Offset))},
	)}
	return c.Client.ProduceSync(ctx, dlq).FirstErr()
}
