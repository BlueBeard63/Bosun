package eventamqpmod

import (
	"context"
	"errors"
	"testing"

	"github.com/bluebeard63/bosun/modules/eventmod"
	amqp "github.com/rabbitmq/amqp091-go"
)

// recordingAck records what the delivery did with the original message.
type recordingAck struct{ acks, nacks, requeues int }

func (r *recordingAck) Ack(uint64, bool) error { r.acks++; return nil }
func (r *recordingAck) Nack(_ uint64, _ bool, requeue bool) error {
	r.nacks++
	if requeue {
		r.requeues++
	}
	return nil
}
func (r *recordingAck) Reject(uint64, bool) error { return nil }

// When the requeued copy can't be published, the original must not be acked:
// it goes back to the broker and Nack reports the failure.
func TestNackRequeueFailureKeepsOriginal(t *testing.T) {
	b := &Bus{Opts: &Options{}, closed: true} // every publish fails with ErrClosed
	ack := &recordingAck{}
	d := &delivery{bus: b, queue: "q", amqpDel: amqp.Delivery{Acknowledger: ack}, msg: eventmod.Message{Subject: "s", Attempt: 1}}

	err := d.Nack(context.Background(), true)
	if !errors.Is(err, eventmod.ErrClosed) {
		t.Fatalf("Nack error = %v, want ErrClosed", err)
	}
	if ack.acks != 0 {
		t.Fatalf("original was acked %d times after a failed requeue", ack.acks)
	}
	if ack.requeues != 1 {
		t.Fatalf("original requeued %d times, want 1", ack.requeues)
	}
}

func TestNackWithoutRequeueRejects(t *testing.T) {
	ack := &recordingAck{}
	d := &delivery{bus: &Bus{Opts: &Options{}}, queue: "q", amqpDel: amqp.Delivery{Acknowledger: ack}}
	if err := d.Nack(context.Background(), false); err != nil {
		t.Fatalf("Nack: %v", err)
	}
	if ack.acks != 0 || ack.nacks != 1 || ack.requeues != 0 {
		t.Fatalf("got acks=%d nacks=%d requeues=%d, want a single non-requeue nack", ack.acks, ack.nacks, ack.requeues)
	}
}
