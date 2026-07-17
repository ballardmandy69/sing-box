package anytls

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestIsWebPreface(t *testing.T) {
	testCases := []struct {
		name   string
		prefix []byte
		want   bool
	}{
		{"http2", http2ClientPreface, true},
		{"http1", []byte("GET / HTTP/1.1\r\n"), true},
		{"http1 long request prefix", []byte("GET /a/long/path/without/a/complete/request/line"), true},
		{"unknown incomplete", []byte("CUSTOM / HTTP/1.1"), false},
		{"random", bytes.Repeat([]byte{0xA5}, 32), false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isWebPreface(testCase.prefix); got != testCase.want {
				t.Fatalf("isWebPreface() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestPrepareConnectionRoutesHTTP2Fallback(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	result := make(chan struct {
		conn        net.Conn
		disposition prefaceDisposition
		err         error
	}, 1)
	go func() {
		conn, disposition, _, err := prepareConnection(serverConn, time.Second, 0, true)
		result <- struct {
			conn        net.Conn
			disposition prefaceDisposition
			err         error
		}{conn, disposition, err}
	}()

	if _, err := clientConn.Write(http2ClientPreface); err != nil {
		t.Fatal(err)
	}
	prepared := <-result
	if prepared.err != nil {
		t.Fatal(prepared.err)
	}
	if prepared.disposition != prefaceFallbackImmediate {
		t.Fatalf("HTTP/2 disposition = %d, want immediate fallback", prepared.disposition)
	}
	replayed := make([]byte, len(http2ClientPreface))
	if _, err := io.ReadFull(prepared.conn, replayed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed, http2ClientPreface) {
		t.Fatal("HTTP/2 preface was not replayed intact")
	}
}

func TestPrepareConnectionKeepsAnyTLSAuthentication(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	result := make(chan struct {
		conn        net.Conn
		disposition prefaceDisposition
		err         error
	}, 1)
	go func() {
		conn, disposition, _, err := prepareConnection(serverConn, time.Second, 0, true)
		result <- struct {
			conn        net.Conn
			disposition prefaceDisposition
			err         error
		}{conn, disposition, err}
	}()

	authentication := bytes.Repeat([]byte{0xA5}, 32)
	if _, err := clientConn.Write(authentication[:13]); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write(authentication[13:]); err != nil {
		t.Fatal(err)
	}
	prepared := <-result
	if prepared.err != nil {
		t.Fatal(prepared.err)
	}
	if prepared.disposition != prefaceAnyTLS {
		t.Fatalf("AnyTLS disposition = %d, want AnyTLS", prepared.disposition)
	}
	replayed := make([]byte, len(authentication))
	if _, err := io.ReadFull(prepared.conn, replayed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed, authentication) {
		t.Fatal("AnyTLS authentication was not replayed intact")
	}
}

func TestPrepareConnectionDelaysPartialUnknownFallback(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	result := make(chan prefaceDisposition, 1)
	go func() {
		_, disposition, _, _ := prepareConnection(serverConn, time.Second, 0, true)
		result <- disposition
	}()
	if _, err := clientConn.Write(bytes.Repeat([]byte{0xA5}, 31)); err != nil {
		t.Fatal(err)
	}
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}

	if disposition := <-result; disposition != prefaceFallbackDelayed {
		t.Fatalf("partial unknown disposition = %d, want delayed fallback", disposition)
	}
}

func TestRandomizedTimeoutRange(t *testing.T) {
	const (
		timeout = 5 * time.Second
		jitter  = 2 * time.Second
	)
	for range 100 {
		randomized := randomizedTimeout(timeout, jitter)
		if randomized < timeout-jitter || randomized > timeout+jitter {
			t.Fatalf("randomized timeout %s outside expected range", randomized)
		}
	}
}
