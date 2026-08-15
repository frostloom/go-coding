package queue

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// RedisQueue is a Queue backed by a Redis List (LPUSH / BRPOP), implemented
// with a hand-written RESP2 client. It deliberately avoids the go-redis SDK:
// the queue needs exactly three commands, and pulling a full client library
// for that is more dependency than value. RESP2 is simple enough to write
// correctly in ~150 lines, and keeping the module stdlib-only is consistent
// with the project's "from scratch" stance (hand-written OpenAI client,
// JSON-RPC, LCS diff).
type RedisQueue struct {
	addr string // host:port
	key  string // Redis List key
}

// NewRedisQueue builds a queue on the given Redis List. The server is
// contacted lazily (per operation), so constructing one never fails.
func NewRedisQueue(addr, key string) *RedisQueue {
	return &RedisQueue{addr: addr, key: key}
}

// Close is a no-op: connections are opened per operation, so there is no
// persistent state to release.
func (q *RedisQueue) Close() error { return nil }

// Ping verifies the server answers PING.
func (q *RedisQueue) Ping(ctx context.Context) error {
	c, err := dial(ctx, q.addr)
	if err != nil {
		return err
	}
	defer c.Close()
	w := bufio.NewWriter(c)
	if err := writeCmd(w, "PING"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = readReply(bufio.NewReader(c))
	return err
}

// Push appends a payload to the head of the List via LPUSH. Redis Lists are
// natural FIFOs when LPUSH is matched with BRPOP.
func (q *RedisQueue) Push(ctx context.Context, payload []byte) error {
	c, err := dial(ctx, q.addr)
	if err != nil {
		return err
	}
	defer c.Close()
	w := bufio.NewWriter(c)
	if err := writeCmd(w, "LPUSH", q.key, string(payload)); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = readReply(bufio.NewReader(c))
	return err
}

// BlockingPop pops from the tail via BRPOP with a short server-side timeout
// (1s) and loops until an item arrives or ctx is done. Each attempt uses a
// fresh connection: two concurrent workers must never issue BRPOP on the
// same socket.
func (q *RedisQueue) BlockingPop(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := dial(ctx, q.addr)
		if err != nil {
			return nil, err
		}
		v, err := q.popOnce(c)
		c.Close()
		if err != nil {
			return nil, err
		}
		if v.kind == '*' && len(v.arr) == 2 {
			return []byte(v.arr[1]), nil
		}
		// nil array → the 1s server timeout expired with an empty list;
		// re-issue BRPOP.
	}
}

// popOnce issues one BRPOP and returns the decoded reply.
func (q *RedisQueue) popOnce(c net.Conn) (respValue, error) {
	_ = c.SetDeadline(time.Now().Add(2 * time.Second)) // > server 1s timeout
	w := bufio.NewWriter(c)
	if err := writeCmd(w, "BRPOP", q.key, "1"); err != nil {
		return respValue{}, err
	}
	if err := w.Flush(); err != nil {
		return respValue{}, err
	}
	return readReply(bufio.NewReader(c))
}

// --- RESP2 wire format ---

// respValue is one decoded RESP2 reply.
type respValue struct {
	kind byte // '+','-',':','$','*','N'(nil)
	str  string
	arr  []string
}

// dial opens a connection honoring ctx.
func dial(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: DefaultTimeout}
	return d.DialContext(ctx, "tcp", addr)
}

// writeCmd encodes args as a RESP2 command array.
func writeCmd(w *bufio.Writer, args ...string) error {
	if _, err := w.WriteString("*" + strconv.Itoa(len(args)) + "\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		if _, err := w.WriteString("$" + strconv.Itoa(len(a)) + "\r\n"); err != nil {
			return err
		}
		if _, err := w.WriteString(a + "\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// readReply decodes a single RESP2 reply.
func readReply(r *bufio.Reader) (respValue, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return respValue{}, err
	}
	if len(line) < 3 {
		return respValue{}, fmt.Errorf("queue: short RESP line %q", line)
	}
	body := line[1 : len(line)-2]
	switch line[0] {
	case '+', '-', ':':
		return respValue{kind: line[0], str: body}, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil {
			return respValue{}, fmt.Errorf("queue: bad bulk length %q", body)
		}
		if n < 0 {
			return respValue{kind: 'N'}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return respValue{}, err
		}
		return respValue{kind: '$', str: string(buf[:n])}, nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil {
			return respValue{}, fmt.Errorf("queue: bad array length %q", body)
		}
		if n < 0 {
			return respValue{kind: 'N'}, nil
		}
		arr := make([]string, 0, n)
		for i := 0; i < n; i++ {
			el, err := readReply(r)
			if err != nil {
				return respValue{}, err
			}
			arr = append(arr, el.str)
		}
		return respValue{kind: '*', arr: arr}, nil
	}
	return respValue{}, fmt.Errorf("queue: unknown RESP type %q", line[0])
}

var _ Queue = (*RedisQueue)(nil)
