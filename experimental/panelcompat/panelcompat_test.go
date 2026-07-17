package panelcompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sagernet/sing-box/include"
)

func TestRunCheckLegacyConfig(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("token") != "test-key" ||
			request.URL.Query().Get("node_id") != "111" ||
			request.URL.Query().Get("node_type") != "anytls" {
			http.Error(writer, "bad query", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/server/UniProxy/config":
			_, _ = writer.Write([]byte(`{
				"protocol":"anytls",
				"listen_ip":"0.0.0.0",
				"server_port":15019,
				"server_name":"example.com",
				"padding_scheme":["stop=8","0=30-30"]
			}`))
		case "/api/v1/server/UniProxy/user":
			_, _ = writer.Write([]byte(`{"users":[{"id":7,"uuid":"test-password","speed_limit":0,"device_limit":0}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer panel.Close()

	directory := t.TempDir()
	basePath := filepath.Join(directory, "config.json")
	serverPath := filepath.Join(directory, "a.yml")
	baseConfig := `{
		"dns": {
			"independent_cache": true,
			"servers": [
				{"tag":"DefaultDNS","address":"local"},
				{"tag":"PublicDNS","address":"8.8.8.8"}
			],
			"rules": [{"domain_keyword":["example"],"server":"PublicDNS"}]
		},
		"log": {"level":"error"},
		"experimental": {"debug": {}},
		"inbounds": [],
		"outbounds": [
			{"tag":"DefaultOut","type":"direct","domain_strategy":"prefer_ipv4"},
			{"tag":"block","type":"block"}
		],
		"route": {
			"ip_on_demand": true,
			"final":"DefaultOut",
			"geoip":{"download_url":"https://example.com/geoip.db"},
			"geosite":{"download_url":"https://example.com/geosite.db"},
			"rules":[
				{"geoip":["private"],"outbound":"block"},
				{"port":[25,443],"outbound":"block"}
			]
		}
	}`
	serverConfig := fmt.Sprintf(`DisableAccessLog: true
Nodes:
  - Type: UniProxy
    UpdateInterval: "60s"
    ApiConfig:
      PanelTag: "atls-2"
      ApiHost: %s
      ApiKey: test-key
      NodeType: Anytls
      NodeID: 111
    CertConfig:
      CertFile: "./zq.crt"
      KeyFile: "./zq.key"
`, panel.URL)
	if err := os.WriteFile(basePath, []byte(baseConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverPath, []byte(serverConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(RunOptions{
		Context:          include.Context(context.Background()),
		BaseConfigPath:   basePath,
		ServerConfigPath: serverPath,
		StateDirectory:   filepath.Join(directory, "state"),
		CheckOnly:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeCount != 1 || result.UserCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	content, err := os.ReadFile(result.RuntimePath)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeConfig map[string]any
	if err = json.Unmarshal(content, &runtimeConfig); err != nil {
		t.Fatal(err)
	}
	dns := runtimeConfig["dns"].(map[string]any)
	servers := dns["servers"].([]any)
	if servers[0].(map[string]any)["type"] != "local" {
		t.Fatal("legacy local DNS was not converted")
	}
	route := runtimeConfig["route"].(map[string]any)
	if _, loaded := route["ip_on_demand"]; loaded {
		t.Fatal("removed route.ip_on_demand was not stripped")
	}
	inbounds := runtimeConfig["inbounds"].([]any)
	inbound := inbounds[0].(map[string]any)
	if inbound["type"] != "anytls" || inbound["listen_port"].(float64) != 15019 {
		t.Fatalf("unexpected inbound: %+v", inbound)
	}
	tlsOptions := inbound["tls"].(map[string]any)
	if tlsOptions["certificate_path"] != filepath.Join(directory, "zq.crt") {
		t.Fatalf("certificate path was not resolved: %+v", tlsOptions)
	}
}

func TestAnyTLSNodesSkipsOtherProtocols(t *testing.T) {
	config := Config{Nodes: []Node{
		{
			Type:      "UniProxy",
			APIConfig: APIConfig{NodeType: "vless"},
		},
		{
			Type:           "uniproxy",
			UpdateInterval: Duration{},
			APIConfig: APIConfig{
				APIHost:  "https://example.com",
				APIKey:   "key",
				NodeType: "ANYTLS",
				NodeID:   9,
			},
		},
	}}
	selected, warnings, err := config.AnyTLSNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(warnings) != 1 {
		t.Fatalf("unexpected selection: selected=%d warnings=%v", len(selected), warnings)
	}
}

func TestParseUserStatName(t *testing.T) {
	label, direction, loaded := parseUserStatName("user>>>panel:1:2>>>traffic>>>downlink")
	if !loaded || label != "panel:1:2" || direction != "downlink" {
		t.Fatalf("unexpected parse result: %q %q %v", label, direction, loaded)
	}
}

func TestBuildEndpoint(t *testing.T) {
	for _, baseURL := range []string{
		"https://panel.example",
		"https://panel.example/api/v1",
		"https://panel.example/api/v1/server/UniProxy",
	} {
		endpoint, err := buildEndpoint(baseURL, "user")
		if err != nil {
			t.Fatal(err)
		}
		if endpoint.Path != "/api/v1/server/UniProxy/user" {
			t.Fatalf("%s produced %s", baseURL, endpoint.Path)
		}
	}
}

func TestExternalCompatibilityExample(t *testing.T) {
	basePath := os.Getenv("PANELCOMPAT_BASE_EXAMPLE")
	serverPath := os.Getenv("PANELCOMPAT_SERVER_EXAMPLE")
	if basePath == "" || serverPath == "" {
		t.Skip("external compatibility example is not configured")
	}
	baseContent, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	selected, _, err := config.AnyTLSNodes()
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[int]*NodeState)
	for _, node := range selected {
		states[node.Index] = &NodeState{
			Index: node.Index,
			Config: map[string]any{
				"protocol":     "anytls",
				"listen_ip":    "0.0.0.0",
				"server_port":  json.Number("15019"),
				"server_name":  "example.com",
				"tls_settings": map[string]any{"server_name": "example.com"},
			},
			Users: []User{{ID: 1, UUID: "test-password"}},
		}
	}
	_, index, err := BuildRuntimeConfig(
		include.Context(context.Background()),
		baseContent,
		serverPath,
		selected,
		states,
		InternalAPI{
			StatsAddress: "127.0.0.1:19090",
			ClashAddress: "127.0.0.1:19091",
			ClashSecret:  "test-secret",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(index.Users) != 1 {
		t.Fatalf("unexpected external example result: nodes=%d users=%d", len(selected), len(index.Users))
	}
}
