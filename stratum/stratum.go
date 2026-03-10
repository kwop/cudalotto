package stratum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kwop/cudalotto/stats"
)

const (
	callTimeout            = 8 * time.Second
	writeTimeout           = 5 * time.Second
	staleConnectionTimeout = 90 * time.Second
)

// Job represents a mining job received from the pool.
type Job struct {
	ID            string
	PrevHash      string   // 64 hex chars
	Coinbase1     string   // hex
	Coinbase2     string   // hex
	MerkleBranches []string // hex strings
	Version       string   // 8 hex chars
	NBits         string   // 8 hex chars
	NTime         string   // 8 hex chars
	CleanJobs     bool
}

// request is a JSON-RPC request.
type request struct {
	ID     uint64      `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// response is a JSON-RPC response.
type response struct {
	ID     *uint64          `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  json.RawMessage  `json:"error"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

// Client is a Stratum v1 client.
type Client struct {
	addr           string
	user           string
	pass           string
	conn           net.Conn
	scanner        *bufio.Scanner
	mu             sync.Mutex
	msgID          uint64
	ExtraNonce1    string
	ExtraNonce2Size int
	difficulty     atomic.Value // float64
	jobChan        chan Job
	submitChan     chan submitReq
	connected      atomic.Bool
	lastMsgTime    atomic.Value // time.Time
	stats          *stats.Stats
}

type submitReq struct {
	jobID       string
	extranonce2 string
	ntime       string
	nonce       string
	result      chan error
}

// NewClient creates a new Stratum client.
func NewClient(addr, user, pass string) *Client {
	c := &Client{
		addr:       addr,
		user:       user,
		pass:       pass,
		submitChan: make(chan submitReq, 16),
	}
	c.difficulty.Store(float64(1))
	return c
}

// SetStats sets the stats tracker.
func (c *Client) SetStats(s *stats.Stats) {
	c.stats = s
}

// Difficulty returns the current mining difficulty.
func (c *Client) Difficulty() float64 {
	return c.difficulty.Load().(float64)
}

// Connect establishes a TCP connection to the pool.
func (c *Client) Connect() error {
	addr := c.addr
	addr = strings.TrimPrefix(addr, "stratum+tcp://")
	addr = strings.TrimPrefix(addr, "tcp://")

	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	// Aggressive TCP keepalive (like cgminer: idle=45s, interval=30s, 3 probes)
	// Dead connection detected in 45 + 30*3 = 135s instead of default 7200s
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		rc, err := tc.SyscallConn()
		if err == nil {
			rc.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE, 45)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL, 30)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT, 3)
			})
		}
	}

	c.conn = conn
	c.scanner = bufio.NewScanner(conn)
	c.scanner.Buffer(make([]byte, 64*1024), 64*1024)
	c.connected.Store(true)
	c.lastMsgTime.Store(time.Now())
	if c.stats != nil {
		c.stats.SetConnected(true)
	}
	return nil
}

// Subscribe sends mining.subscribe and parses the response.
func (c *Client) Subscribe() error {
	resp, err := c.call("mining.subscribe", []interface{}{"cudalotto/1.0"})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	var result []json.RawMessage
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("subscribe parse: %w", err)
	}

	if len(result) < 3 {
		return fmt.Errorf("subscribe: unexpected result length %d", len(result))
	}

	if err := json.Unmarshal(result[1], &c.ExtraNonce1); err != nil {
		return fmt.Errorf("subscribe extranonce1: %w", err)
	}
	if err := json.Unmarshal(result[2], &c.ExtraNonce2Size); err != nil {
		return fmt.Errorf("subscribe extranonce2_size: %w", err)
	}

	log.Printf("[stratum] subscribed, extranonce1=%s, extranonce2_size=%d", c.ExtraNonce1, c.ExtraNonce2Size)
	return nil
}

// Authorize sends mining.authorize.
func (c *Client) Authorize() error {
	resp, err := c.call("mining.authorize", []interface{}{c.user, c.pass})
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}

	var ok bool
	if err := json.Unmarshal(resp.Result, &ok); err != nil || !ok {
		return fmt.Errorf("authorize rejected (user=%s)", c.user)
	}

	log.Printf("[stratum] authorized as %s", c.user)
	return nil
}

// Listen reads messages from the pool and dispatches jobs.
// Blocks until connection is lost.
func (c *Client) Listen(jobChan chan Job) error {
	c.jobChan = jobChan

	for c.scanner.Scan() {
		line := c.scanner.Text()
		if line == "" {
			continue
		}

		c.lastMsgTime.Store(time.Now())

		var msg response
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[stratum] parse error: %v", err)
			continue
		}

		// Server-initiated method call (notification)
		if msg.Method != "" {
			c.handleNotification(msg.Method, msg.Params)
			continue
		}
	}

	c.connected.Store(false)
	if c.stats != nil {
		c.stats.SetConnected(false)
	}
	if err := c.scanner.Err(); err != nil {
		return fmt.Errorf("connection lost: %w", err)
	}
	return fmt.Errorf("connection closed by pool")
}

// IsConnected returns whether the client has an active connection.
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// IsStale returns true if no message has been received from the pool recently.
func (c *Client) IsStale() bool {
	t, ok := c.lastMsgTime.Load().(time.Time)
	if !ok {
		return true
	}
	return time.Since(t) > staleConnectionTimeout
}

// Submit sends a mining.submit to the pool.
func (c *Client) Submit(jobID, extranonce2, ntime, nonce string) error {
	// Fast-fail if connection is stale (avoid blocking on dead socket)
	if c.IsStale() {
		age := time.Duration(0)
		if t, ok := c.lastMsgTime.Load().(time.Time); ok {
			age = time.Since(t)
		}
		return fmt.Errorf("submit: connection stale (no message for %v)", age.Round(time.Second))
	}

	params := []interface{}{c.user, jobID, extranonce2, ntime, nonce}
	resp, err := c.call("mining.submit", params)
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}

	var accepted bool
	if err := json.Unmarshal(resp.Result, &accepted); err != nil {
		return fmt.Errorf("submit parse: %w", err)
	}

	if accepted {
		log.Printf("[stratum] share ACCEPTED (job=%s nonce=%s)", jobID, nonce)
		if c.stats != nil {
			c.stats.SharesAccepted.Add(1)
		}
	} else {
		log.Printf("[stratum] share REJECTED (job=%s nonce=%s error=%s)", jobID, nonce, string(resp.Error))
		if c.stats != nil {
			c.stats.SharesRejected.Add(1)
		}
	}

	return nil
}

// Close closes the connection.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "mining.notify":
		c.handleNotify(params)
	case "mining.set_difficulty":
		c.handleSetDifficulty(params)
	default:
		log.Printf("[stratum] unknown method: %s", method)
	}
}

func (c *Client) handleNotify(params json.RawMessage) {
	var p []json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil || len(p) < 9 {
		log.Printf("[stratum] notify parse error")
		return
	}

	var job Job
	json.Unmarshal(p[0], &job.ID)
	json.Unmarshal(p[1], &job.PrevHash)
	json.Unmarshal(p[2], &job.Coinbase1)
	json.Unmarshal(p[3], &job.Coinbase2)

	var branches []string
	json.Unmarshal(p[4], &branches)
	job.MerkleBranches = branches

	json.Unmarshal(p[5], &job.Version)
	json.Unmarshal(p[6], &job.NBits)
	json.Unmarshal(p[7], &job.NTime)
	json.Unmarshal(p[8], &job.CleanJobs)

	log.Printf("[stratum] new job: %s (clean=%v)", job.ID, job.CleanJobs)
	if c.stats != nil {
		c.stats.JobsReceived.Add(1)
		c.stats.SetJobID(job.ID)
	}

	if c.jobChan == nil {
		return // not listening yet, discard early jobs
	}

	select {
	case c.jobChan <- job:
	default:
		// Drop old job if channel is full
		select {
		case <-c.jobChan:
		default:
		}
		c.jobChan <- job
	}
}

func (c *Client) handleSetDifficulty(params json.RawMessage) {
	var p []float64
	if err := json.Unmarshal(params, &p); err != nil || len(p) < 1 {
		log.Printf("[stratum] set_difficulty parse error")
		return
	}
	c.difficulty.Store(p[0])
	if c.stats != nil {
		c.stats.SetDifficulty(p[0])
	}
	log.Printf("[stratum] difficulty set to %f", p[0])
}

func (c *Client) call(method string, params interface{}) (*response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.msgID++
	req := request{
		ID:     c.msgID,
		Method: method,
		Params: params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}

	// Read responses, skipping server notifications until we get our reply
	c.conn.SetReadDeadline(time.Now().Add(callTimeout))
	defer c.conn.SetReadDeadline(time.Time{}) // clear deadline for Listen()
	for {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("connection closed")
		}

		c.lastMsgTime.Store(time.Now())

		var resp response
		if err := json.Unmarshal([]byte(c.scanner.Text()), &resp); err != nil {
			return nil, fmt.Errorf("response parse: %w", err)
		}

		// Server notification (no ID) — handle inline and keep reading
		if resp.ID == nil && resp.Method != "" {
			c.handleNotification(resp.Method, resp.Params)
			continue
		}

		return &resp, nil
	}
}
