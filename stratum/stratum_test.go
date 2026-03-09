package stratum

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// mockPool creates a listener and returns the address and a function to accept one connection.
func mockPool(t *testing.T) (string, func() net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), func() net.Conn {
		conn, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
}

func TestConnect(t *testing.T) {
	addr, accept := mockPool(t)
	go accept()

	c := NewClient(addr, "testuser", "x")
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer c.Close()
}

func TestConnectBadAddr(t *testing.T) {
	c := NewClient("127.0.0.1:1", "testuser", "x")
	err := c.Connect()
	if err == nil {
		t.Fatal("expected error connecting to bad address")
	}
}

func TestConnectStripsProtocol(t *testing.T) {
	addr, accept := mockPool(t)
	go accept()

	c := NewClient("stratum+tcp://"+addr, "testuser", "x")
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() with stratum+tcp:// prefix failed: %v", err)
	}
	defer c.Close()
}

func TestSubscribe(t *testing.T) {
	addr, accept := mockPool(t)

	go func() {
		conn := accept()
		defer conn.Close()
		scanner := bufio.NewScanner(conn)

		// Read subscribe request
		if !scanner.Scan() {
			t.Error("expected subscribe request")
			return
		}
		var req request
		json.Unmarshal([]byte(scanner.Text()), &req)

		// Respond
		resp := `{"id":1,"result":[[["mining.notify","sub1"]],"aabb0011",4],"error":null}` + "\n"
		conn.Write([]byte(resp))
	}()

	c := NewClient(addr, "testuser", "x")
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Subscribe(); err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}

	if c.ExtraNonce1 != "aabb0011" {
		t.Errorf("ExtraNonce1 = %q, want %q", c.ExtraNonce1, "aabb0011")
	}
	if c.ExtraNonce2Size != 4 {
		t.Errorf("ExtraNonce2Size = %d, want 4", c.ExtraNonce2Size)
	}
}

func TestAuthorize(t *testing.T) {
	addr, accept := mockPool(t)

	go func() {
		conn := accept()
		defer conn.Close()
		scanner := bufio.NewScanner(conn)

		// Read authorize request
		if !scanner.Scan() {
			return
		}
		resp := `{"id":1,"result":true,"error":null}` + "\n"
		conn.Write([]byte(resp))
	}()

	c := NewClient(addr, "testuser", "x")
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Authorize(); err != nil {
		t.Fatalf("Authorize() error: %v", err)
	}
}

func TestAuthorizeRejected(t *testing.T) {
	addr, accept := mockPool(t)

	go func() {
		conn := accept()
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Scan()
		conn.Write([]byte(`{"id":1,"result":false,"error":null}` + "\n"))
	}()

	c := NewClient(addr, "testuser", "x")
	c.Connect()
	defer c.Close()

	err := c.Authorize()
	if err == nil {
		t.Fatal("expected authorize to be rejected")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error should mention 'rejected', got: %v", err)
	}
}

func TestCallSkipsNotifications(t *testing.T) {
	addr, accept := mockPool(t)

	go func() {
		conn := accept()
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		scanner.Scan() // read request

		// Send a notification first (no id), then the real response
		conn.Write([]byte(`{"id":null,"method":"mining.set_difficulty","params":[10000]}` + "\n"))
		conn.Write([]byte(`{"id":1,"result":true,"error":null}` + "\n"))
	}()

	c := NewClient(addr, "testuser", "x")
	c.Connect()
	defer c.Close()

	// Authorize should skip the set_difficulty notification
	if err := c.Authorize(); err != nil {
		t.Fatalf("Authorize() should succeed after skipping notification: %v", err)
	}

	// Difficulty should have been set by the skipped notification
	if c.Difficulty() != 10000 {
		t.Errorf("Difficulty() = %f, want 10000", c.Difficulty())
	}
}

func TestHandleNotify(t *testing.T) {
	c := NewClient("", "user", "x")
	jobChan := make(chan Job, 2)
	c.jobChan = jobChan

	notifyParams := `["job123","` +
		`00000000000000000000000000000000000000000000000000000000deadbeef","` +
		`coinbase1hex","coinbase2hex",` +
		`["branch1","branch2"],` +
		`"20000000","1a0ffff0","65a1c0d0",true]`

	c.handleNotify(json.RawMessage(notifyParams))

	select {
	case job := <-jobChan:
		if job.ID != "job123" {
			t.Errorf("job.ID = %q, want %q", job.ID, "job123")
		}
		if job.Version != "20000000" {
			t.Errorf("job.Version = %q, want %q", job.Version, "20000000")
		}
		if job.NBits != "1a0ffff0" {
			t.Errorf("job.NBits = %q, want %q", job.NBits, "1a0ffff0")
		}
		if job.NTime != "65a1c0d0" {
			t.Errorf("job.NTime = %q, want %q", job.NTime, "65a1c0d0")
		}
		if !job.CleanJobs {
			t.Error("job.CleanJobs should be true")
		}
		if len(job.MerkleBranches) != 2 {
			t.Errorf("len(MerkleBranches) = %d, want 2", len(job.MerkleBranches))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job")
	}
}

func TestHandleSetDifficulty(t *testing.T) {
	c := NewClient("", "user", "x")

	if c.Difficulty() != 1.0 {
		t.Fatalf("initial difficulty should be 1.0, got %f", c.Difficulty())
	}

	c.handleSetDifficulty(json.RawMessage(`[5000.0]`))
	if c.Difficulty() != 5000.0 {
		t.Errorf("Difficulty() = %f, want 5000.0", c.Difficulty())
	}
}

func TestHandleNotifyBeforeListening(t *testing.T) {
	c := NewClient("", "user", "x")
	// jobChan is nil — should not panic
	notifyParams := `["job1","prevhash","cb1","cb2",[],"20000000","1a0f","6500",false]`
	c.handleNotify(json.RawMessage(notifyParams)) // must not panic
}
