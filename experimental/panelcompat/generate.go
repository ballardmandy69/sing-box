package panelcompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
)

type UserBinding struct {
	NodeIndex int
	UserID    int64
}

type RuntimeIndex struct {
	Users map[string]UserBinding
}

type InternalAPI struct {
	StatsAddress string
	ClashAddress string
	ClashSecret  string
}

func BuildRuntimeConfig(
	ctx context.Context,
	baseContent []byte,
	serverConfigPath string,
	selected []SelectedNode,
	states map[int]*NodeState,
	internal InternalAPI,
) ([]byte, RuntimeIndex, error) {
	base, err := singjson.UnmarshalExtendedContext[map[string]any](ctx, baseContent)
	if err != nil {
		return nil, RuntimeIndex{}, fmt.Errorf("decode base config: %w", err)
	}
	if err = normalizeLegacyBase(base); err != nil {
		return nil, RuntimeIndex{}, err
	}
	index := RuntimeIndex{Users: make(map[string]UserBinding)}
	inbounds, _ := base["inbounds"].([]any)
	if len(inbounds) != 0 {
		return nil, RuntimeIndex{}, fmt.Errorf("base config inbounds must be empty; panel mode creates pure AnyTLS inbounds")
	}
	inbounds = make([]any, 0, len(selected))
	statsUsers := make([]string, 0)
	reportAlive := false
	for _, selectedNode := range selected {
		state := states[selectedNode.Index]
		if state == nil {
			return nil, RuntimeIndex{}, fmt.Errorf("missing state for %s", selectedNode.Tag())
		}
		inbound, bindings, err := buildInbound(serverConfigPath, selectedNode, state)
		if err != nil {
			return nil, RuntimeIndex{}, err
		}
		inbounds = append(inbounds, inbound)
		for label, binding := range bindings {
			index.Users[label] = binding
			statsUsers = append(statsUsers, label)
		}
		reportAlive = reportAlive || selectedNode.API.ReportAlive
	}
	base["inbounds"] = inbounds

	experimental := ensureObject(base, "experimental")
	experimental["v2ray_api"] = map[string]any{
		"listen": internal.StatsAddress,
		"stats": map[string]any{
			"enabled": true,
			"users":   statsUsers,
		},
	}
	if reportAlive {
		experimental["clash_api"] = map[string]any{
			"external_controller": internal.ClashAddress,
			"secret":              internal.ClashSecret,
		}
	}

	content, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return nil, RuntimeIndex{}, fmt.Errorf("encode runtime config: %w", err)
	}
	if _, err = singjson.UnmarshalExtendedContext[option.Options](ctx, content); err != nil {
		return nil, RuntimeIndex{}, fmt.Errorf("validate generated runtime config: %w", err)
	}
	return append(content, '\n'), index, nil
}

func buildInbound(serverConfigPath string, selected SelectedNode, state *NodeState) (map[string]any, map[string]UserBinding, error) {
	nodeConfig := state.Config
	protocol := strings.TrimSpace(stringValue(nodeConfig["protocol"]))
	if protocol != "" && !strings.EqualFold(protocol, "anytls") {
		return nil, nil, fmt.Errorf("%s panel protocol is %q, expected AnyTLS", selected.Tag(), protocol)
	}
	listenIP := strings.TrimSpace(selected.Node.ListenIP)
	if listenIP == "" {
		listenIP = strings.TrimSpace(stringValue(nodeConfig["listen_ip"]))
	}
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	listenPort := selected.Node.ListenPort
	if listenPort == 0 {
		port, err := uint16Value(nodeConfig["server_port"])
		if err != nil || port == 0 {
			return nil, nil, fmt.Errorf("%s panel returned an invalid server_port", selected.Tag())
		}
		listenPort = port
	}

	users := make([]any, 0, len(state.Users))
	bindings := make(map[string]UserBinding, len(state.Users))
	for _, user := range state.Users {
		label := fmt.Sprintf("%s:%d:%d", selected.Tag(), selected.API.NodeID, user.ID)
		users = append(users, map[string]any{
			"name":     label,
			"password": user.UUID,
		})
		bindings[label] = UserBinding{NodeIndex: selected.Index, UserID: user.ID}
	}
	inbound := map[string]any{
		"type":        "anytls",
		"tag":         selected.Tag(),
		"listen":      listenIP,
		"listen_port": listenPort,
		"users":       users,
	}

	paddingScheme := []string(selected.Node.PaddingScheme)
	if len(paddingScheme) == 0 {
		paddingScheme = stringSlice(nodeConfig["padding_scheme"])
	}
	if len(paddingScheme) > 0 {
		inbound["padding_scheme"] = paddingScheme
	}
	if selected.Node.ServerPadding != nil {
		inbound["server_padding"] = *selected.Node.ServerPadding
	} else {
		inbound["server_padding"] = true
	}
	if selected.Node.AuthenticationTimeout.Duration > 0 {
		inbound["authentication_timeout"] = selected.Node.AuthenticationTimeout.Duration.String()
	}
	if selected.Node.AuthenticationTimeoutJitter.Duration > 0 {
		inbound["authentication_timeout_jitter"] = selected.Node.AuthenticationTimeoutJitter.Duration.String()
	}

	if !selected.Node.DisableTLS {
		certificatePath := ResolveConfigPath(serverConfigPath, selected.Node.CertConfig.CertFile)
		keyPath := ResolveConfigPath(serverConfigPath, selected.Node.CertConfig.KeyFile)
		if certificatePath == "" || keyPath == "" {
			return nil, nil, fmt.Errorf("%s requires CertConfig.CertFile and CertConfig.KeyFile", selected.Tag())
		}
		serverName := strings.TrimSpace(stringValue(nodeConfig["server_name"]))
		if serverName == "" {
			if tlsSettings, loaded := nodeConfig["tls_settings"].(map[string]any); loaded {
				serverName = strings.TrimSpace(stringValue(tlsSettings["server_name"]))
			}
		}
		alpn := append([]string(nil), selected.Node.ALPN...)
		if len(alpn) == 0 {
			alpn = []string{"h2", "http/1.1"}
		}
		tlsOptions := map[string]any{
			"enabled":          true,
			"certificate_path": certificatePath,
			"key_path":         keyPath,
			"alpn":             alpn,
		}
		if serverName != "" {
			tlsOptions["server_name"] = serverName
		}
		if selected.Node.TLSJSON != "" {
			var override map[string]any
			if err := json.Unmarshal([]byte(selected.Node.TLSJSON), &override); err != nil {
				return nil, nil, fmt.Errorf("%s TLSJson: %w", selected.Tag(), err)
			}
			for key, value := range override {
				tlsOptions[key] = value
			}
		}
		inbound["tls"] = tlsOptions
	}

	if selected.Node.Fallback != "" {
		endpoint, err := parseServerOptions(selected.Node.Fallback)
		if err != nil {
			return nil, nil, fmt.Errorf("%s Fallback: %w", selected.Tag(), err)
		}
		inbound["fallback"] = endpoint
	}
	if len(selected.Node.FallbackForALPN) > 0 {
		fallbacks, err := parseServerOptionsMap(selected.Node.FallbackForALPN)
		if err != nil {
			return nil, nil, fmt.Errorf("%s FallbackForALPN: %w", selected.Tag(), err)
		}
		inbound["fallback_for_alpn"] = fallbacks
	}
	if len(selected.Node.FallbackForServerName) > 0 {
		fallbacks, err := parseServerOptionsMap(selected.Node.FallbackForServerName)
		if err != nil {
			return nil, nil, fmt.Errorf("%s FallbackForServerName: %w", selected.Tag(), err)
		}
		inbound["fallback_for_server_name"] = fallbacks
	}
	return inbound, bindings, nil
}

func normalizeLegacyBase(base map[string]any) error {
	if route, loaded := base["route"].(map[string]any); loaded {
		delete(route, "ip_on_demand")
	}
	dns, loaded := base["dns"].(map[string]any)
	if !loaded {
		return nil
	}
	servers, loaded := dns["servers"].([]any)
	if !loaded {
		return nil
	}
	for index, rawServer := range servers {
		server, loaded := rawServer.(map[string]any)
		if !loaded || stringValue(server["type"]) != "" {
			continue
		}
		address := strings.TrimSpace(stringValue(server["address"]))
		if address == "" {
			return fmt.Errorf("dns.servers[%d] legacy address is empty", index)
		}
		delete(server, "address")
		switch {
		case strings.EqualFold(address, "local"):
			server["type"] = "local"
		case strings.Contains(address, "://"):
			parsed, err := url.Parse(address)
			if err != nil {
				return fmt.Errorf("dns.servers[%d]: %w", index, err)
			}
			switch strings.ToLower(parsed.Scheme) {
			case "udp", "tcp", "tls", "quic", "https", "h3":
				server["type"] = strings.ToLower(parsed.Scheme)
			default:
				return fmt.Errorf("dns.servers[%d] unsupported legacy scheme %q", index, parsed.Scheme)
			}
			server["server"] = parsed.Hostname()
			if parsed.Port() != "" {
				port, err := strconv.ParseUint(parsed.Port(), 10, 16)
				if err != nil {
					return fmt.Errorf("dns.servers[%d]: %w", index, err)
				}
				server["server_port"] = port
			}
			if parsed.Path != "" && parsed.Path != "/" {
				server["path"] = parsed.Path
			}
		default:
			host, portText, err := net.SplitHostPort(address)
			server["type"] = "udp"
			if err == nil {
				server["server"] = host
				port, parseErr := strconv.ParseUint(portText, 10, 16)
				if parseErr != nil {
					return fmt.Errorf("dns.servers[%d]: %w", index, parseErr)
				}
				server["server_port"] = port
			} else {
				server["server"] = address
			}
		}
	}
	return nil
}

func parseServerOptionsMap(values map[string]string) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for key, value := range values {
		endpoint, err := parseServerOptions(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		result[key] = endpoint
	}
	return result, nil
}

func parseServerOptions(value string) (map[string]any, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("expected host:port: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid port %q", portText)
	}
	return map[string]any{"server": host, "server_port": port}, nil
}

func ensureObject(parent map[string]any, key string) map[string]any {
	if object, loaded := parent[key].(map[string]any); loaded {
		return object
	}
	object := make(map[string]any)
	parent[key] = object
	return object
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func uint16Value(value any) (uint16, error) {
	var raw string
	switch typed := value.(type) {
	case json.Number:
		raw = typed.String()
	case float64:
		raw = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		raw = strconv.Itoa(typed)
	case int64:
		raw = strconv.FormatInt(typed, 10)
	case string:
		raw = typed
	default:
		return 0, fmt.Errorf("not a number")
	}
	parsed, err := strconv.ParseUint(raw, 10, 16)
	return uint16(parsed), err
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	case string:
		var result []string
		for _, line := range strings.Split(strings.ReplaceAll(typed, "\r\n", "\n"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				result = append(result, line)
			}
		}
		return result
	default:
		return nil
	}
}

func RuntimePath(stateDirectory string) string {
	return filepath.Join(stateDirectory, "runtime.json")
}
