package kafka

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

func TestWriter(t *testing.T) {
	tests := []struct {
		scenario string
		function func(*testing.T)
	}{
		/*{
			scenario: "closing a writer right after creating it returns promptly with no error",
			function: testWriterClose,
		},

		{
			scenario: "writing 1 message through a writer using round-robin balancing produces 1 message to the first partition",
			function: testWriterRoundRobin1,
		},

		{
			scenario: "running out of max attempts should return an error",
			function: testWriterMaxAttemptsErr,
		},
		*/
		{
			scenario: "write to unrecheable server should not lock WriteMessages method",
			function: testWriterUnreacheableServer,
		},
	}

	t.Parallel()
	// Kafka takes a while to create the initial topics and partitions...
	//time.Sleep(15 * time.Second)

	for _, test := range tests {
		testFunc := test.function
		t.Run(test.scenario, func(t *testing.T) {
			t.Parallel()
			testFunc(t)
		})
	}
}

func newTestWriter(config WriterConfig) *Writer {
	if len(config.Brokers) == 0 {
		config.Brokers = []string{"localhost:9092"}
	}
	return NewWriter(config)
}

func testWriterClose(t *testing.T) {
	w := newTestWriter(WriterConfig{
		Topic: "test-writer-0",
	})

	if err := w.Close(); err != nil {
		t.Error(err)
	}
}

func testWriterRoundRobin1(t *testing.T) {
	const topic = "test-writer-1"

	offset, err := readOffset(topic, 0)
	if err != nil {
		t.Fatal(err)
	}

	w := newTestWriter(WriterConfig{
		Topic:    topic,
		Balancer: &RoundRobin{},
	})
	defer w.Close()

	if err := w.WriteMessages(context.Background(), Message{
		Value: []byte("Hello World!"),
	}); err != nil {
		t.Error(err)
		return
	}

	msgs, err := readPartition(topic, 0, offset)

	if err != nil {
		t.Error("error reading partition", err)
		return
	}

	if len(msgs) != 1 {
		t.Error("bad messages in partition", msgs)
		return
	}

	for _, m := range msgs {
		if string(m.Value) != "Hello World!" {
			t.Error("bad messages in partition", msgs)
			break
		}
	}
}

func sendmsg(ctx context.Context, w *Writer) {
	w.WriteMessages(ctx, Message{Value: []byte("Hello World!")})
	time.Sleep(100 * time.Millisecond)
}

func testWriterUnreacheableServer(t *testing.T) {
	w := newTestWriter(WriterConfig{
		Topic:             "test-writer-2",
		Brokers:           []string{"127.0.0.1:9999"},
		QueueCapacity:     1,
		BatchSize:         1,
		MaxAttempts:       1,
		RebalanceInterval: 1000 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log.Println(time.Now())

	for i := 0; i < 10; i++ {
		sendmsg(ctx, w)
	}
	log.Println(time.Now())

	errch := make(chan error)
	go func() {
		err := w.WriteMessages(ctx, Message{Value: []byte("Hello World!")})
		errch <- err
	}()

	tickch := time.After(1 * time.Second)
	select {
	case err := <-errch:
		t.Error(err)
	case <-tickch:
		t.Error("WriteMessages blocked when using an unreachable server")
	}

}

type fakeWriter struct{}

func (f *fakeWriter) messages() chan<- writerMessage {
	ch := make(chan writerMessage, 1)

	go func() {
		for {
			msg := <-ch
			msg.res <- &writerError{
				err: errors.New("bad attempt"),
			}
		}
	}()

	return ch
}

func (f *fakeWriter) close() {

}

func testWriterMaxAttemptsErr(t *testing.T) {
	const topic = "test-writer-1"

	w := newTestWriter(WriterConfig{
		Topic:       topic,
		MaxAttempts: 1,
		Balancer:    &RoundRobin{},
		newPartitionWriter: func(p int, config WriterConfig) partitionWriter {
			return &fakeWriter{}
		},
	})
	defer w.Close()

	if err := w.WriteMessages(context.Background(), Message{
		Value: []byte("Hello World!"),
	}); err == nil {
		t.Error("expected error")
		return
	} else if err != nil {
		if !strings.Contains(err.Error(), "bad attempt") {
			t.Errorf("unexpected error: %s", err)
			return
		}
	}
}

func readOffset(topic string, partition int) (offset int64, err error) {
	var conn *Conn

	if conn, err = DialLeader(context.Background(), "tcp", "localhost:9092", topic, partition); err != nil {
		return
	}
	defer conn.Close()

	offset, err = conn.ReadLastOffset()
	return
}

func readPartition(topic string, partition int, offset int64) (msgs []Message, err error) {
	var conn *Conn

	if conn, err = DialLeader(context.Background(), "tcp", "localhost:9092", topic, partition); err != nil {
		return
	}
	defer conn.Close()

	conn.Seek(offset, 1)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	batch := conn.ReadBatch(0, 1000000000)
	defer batch.Close()

	for {
		var msg Message

		if msg, err = batch.ReadMessage(); err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}

		msgs = append(msgs, msg)
	}
}
