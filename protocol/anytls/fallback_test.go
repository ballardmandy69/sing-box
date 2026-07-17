package anytls

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestValidateFallbackALPN(t *testing.T) {
	fallbacks := map[string]*option.ServerOptions{
		"h2": {
			Server:     "127.0.0.1",
			ServerPort: 8082,
		},
	}
	if err := validateFallbackALPN([]string{"h2", "http/1.1"}, fallbacks); err != nil {
		t.Fatal(err)
	}
	if err := validateFallbackALPN([]string{"http/1.1"}, fallbacks); err == nil {
		t.Fatal("unadvertised fallback ALPN was accepted")
	}
	if err := validateFallbackALPN([]string{"h2"}, map[string]*option.ServerOptions{"h2": nil}); err == nil {
		t.Fatal("nil fallback destination was accepted")
	}
}

func TestSelectFallbackAddress(t *testing.T) {
	defaultAddr := M.ParseSocksaddrHostPort("127.0.0.1", 8080)
	sniAddr := M.ParseSocksaddrHostPort("127.0.0.1", 8081)
	h2Addr := M.ParseSocksaddrHostPort("127.0.0.1", 8082)
	inbound := &Inbound{
		fallbackAddr: defaultAddr,
		fallbackAddrServerName: map[string]M.Socksaddr{
			"example.com": sniAddr,
		},
		fallbackAddrTLSNextProto: map[string]M.Socksaddr{
			"h2":       h2Addr,
			"http/1.1": defaultAddr,
		},
	}

	testCases := []struct {
		name       string
		serverName string
		nextProto  string
		want       M.Socksaddr
		wantReject bool
	}{
		{"HTTP/2 overrides SNI destination", "example.com", "h2", h2Addr, false},
		{"HTTP/1.1 destination", "EXAMPLE.COM", "http/1.1", defaultAddr, false},
		{"unknown SNI rejected", "invalid.example", "h2", M.Socksaddr{}, true},
		{"unknown ALPN rejected", "example.com", "acme/1", M.Socksaddr{}, true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, rejectionReason := inbound.selectFallbackAddress(testCase.serverName, testCase.nextProto)
			if (rejectionReason != "") != testCase.wantReject {
				t.Fatalf("rejection reason = %q, want rejection %v", rejectionReason, testCase.wantReject)
			}
			if got != testCase.want {
				t.Fatalf("fallback address = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSelectFallbackAddressUsesDefaultWithoutTLSMetadata(t *testing.T) {
	defaultAddr := M.ParseSocksaddrHostPort("127.0.0.1", 8080)
	inbound := &Inbound{fallbackAddr: defaultAddr}

	got, rejectionReason := inbound.selectFallbackAddress("", "")
	if rejectionReason != "" {
		t.Fatal(rejectionReason)
	}
	if got != defaultAddr {
		t.Fatalf("fallback address = %v, want %v", got, defaultAddr)
	}
}

func TestAuthenticationFallbackDelay(t *testing.T) {
	now := time.Unix(100, 0)
	ctx := context.WithValue(context.Background(), authenticationDeadlineContextKey{}, now.Add(2*time.Second))

	if delay := authenticationFallbackDelay(ctx, now); delay != 2*time.Second {
		t.Fatalf("fallback delay = %s, want 2s", delay)
	}
	if delay := authenticationFallbackDelay(ctx, now.Add(3*time.Second)); delay != 0 {
		t.Fatalf("expired fallback delay = %s, want 0", delay)
	}
	if delay := authenticationFallbackDelay(context.Background(), now); delay != 0 {
		t.Fatalf("fallback delay without deadline = %s, want 0", delay)
	}
}
