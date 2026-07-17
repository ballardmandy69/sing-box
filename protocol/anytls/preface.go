package anytls

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"math/big"
	"net"
	"time"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
)

const (
	defaultAuthenticationTimeout       = 5 * time.Second
	defaultAuthenticationTimeoutJitter = 2 * time.Second
)

var http2ClientPreface = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

var http1MethodPrefixes = [][]byte{
	[]byte("GET "),
	[]byte("POST "),
	[]byte("HEAD "),
	[]byte("PUT "),
	[]byte("DELETE "),
	[]byte("OPTIONS "),
	[]byte("PATCH "),
	[]byte("CONNECT "),
	[]byte("TRACE "),
}

type prefaceDisposition uint8

const (
	prefaceAnyTLS prefaceDisposition = iota
	prefaceFallbackImmediate
	prefaceFallbackDelayed
)

func prepareConnection(conn net.Conn, timeout time.Duration, timeoutJitter time.Duration, fallbackEnabled bool) (net.Conn, prefaceDisposition, time.Time, error) {
	var authenticationDeadline time.Time
	if timeout > 0 {
		authenticationDeadline = time.Now().Add(randomizedTimeout(timeout, timeoutJitter))
		err := conn.SetReadDeadline(authenticationDeadline)
		if err != nil {
			return conn, prefaceAnyTLS, authenticationDeadline, err
		}
	}

	prefix := make([]byte, 0, sha256.Size)
	readBuffer := make([]byte, sha256.Size)
	for len(prefix) < sha256.Size {
		n, err := conn.Read(readBuffer[:sha256.Size-len(prefix)])
		if n > 0 {
			prefix = append(prefix, readBuffer[:n]...)
			if isWebPreface(prefix) {
				_ = conn.SetReadDeadline(time.Time{})
				if !fallbackEnabled {
					return conn, prefaceAnyTLS, authenticationDeadline, io.ErrUnexpectedEOF
				}
				return bufio.NewCachedConn(conn, buf.As(prefix).ToOwned()), prefaceFallbackImmediate, authenticationDeadline, nil
			}
		}
		if err != nil {
			_ = conn.SetReadDeadline(time.Time{})
			if fallbackEnabled && len(prefix) > 0 {
				return bufio.NewCachedConn(conn, buf.As(prefix).ToOwned()), prefaceFallbackDelayed, authenticationDeadline, nil
			}
			return conn, prefaceAnyTLS, authenticationDeadline, err
		}
	}

	_ = conn.SetReadDeadline(time.Time{})
	return bufio.NewCachedConn(conn, buf.As(prefix).ToOwned()), prefaceAnyTLS, authenticationDeadline, nil
}

func isWebPreface(prefix []byte) bool {
	if len(prefix) >= len(http2ClientPreface) && bytes.Equal(prefix[:len(http2ClientPreface)], http2ClientPreface) {
		return true
	}
	for _, methodPrefix := range http1MethodPrefixes {
		if bytes.HasPrefix(prefix, methodPrefix) {
			return true
		}
	}

	lineEnd := bytes.Index(prefix, []byte("\r\n"))
	if lineEnd <= 0 {
		return false
	}
	requestLine := prefix[:lineEnd]
	firstSpace := bytes.IndexByte(requestLine, ' ')
	lastSpace := bytes.LastIndexByte(requestLine, ' ')
	if firstSpace <= 0 || lastSpace <= firstSpace+1 {
		return false
	}
	version := requestLine[lastSpace+1:]
	return bytes.Equal(version, []byte("HTTP/1.0")) || bytes.Equal(version, []byte("HTTP/1.1"))
}

func randomizedTimeout(timeout time.Duration, jitter time.Duration) time.Duration {
	if timeout <= 0 || jitter <= 0 {
		return timeout
	}
	if maxJitter := timeout / 2; jitter > maxJitter {
		jitter = maxJitter
	}
	randomRange := int64(jitter)*2 + 1
	randomValue, err := rand.Int(rand.Reader, big.NewInt(randomRange))
	if err != nil {
		return timeout
	}
	return timeout - jitter + time.Duration(randomValue.Int64())
}
