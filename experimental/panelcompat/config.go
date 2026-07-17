package panelcompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultUpdateInterval = time.Minute

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == 0 {
		return nil
	}
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

type PaddingScheme []string

func (s *PaddingScheme) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				*s = append(*s, line)
			}
		}
		return nil
	case yaml.SequenceNode:
		var values []string
		if err := value.Decode(&values); err != nil {
			return err
		}
		*s = values
		return nil
	default:
		return fmt.Errorf("padding scheme must be a string or string array")
	}
}

type Config struct {
	DisableAccessLog             bool   `yaml:"DisableAccessLog"`
	DisablePrintAccessLog        bool   `yaml:"DisablePrintAccessLog"`
	IPLimit                      uint16 `yaml:"IPLimit"`
	RateLimit                    uint   `yaml:"RateLimit"`
	ConnectionLimit              uint32 `yaml:"ConnectionLimit"`
	ConnectionCleanupIntervalSec uint64 `yaml:"ConnectionCleanupIntervalSec"`
	HotUserCacheSec              int64  `yaml:"HotUserCacheSec"`
	Nodes                        []Node `yaml:"Nodes"`
}

type APIConfig struct {
	PanelTag      string `yaml:"PanelTag"`
	APIHost       string `yaml:"ApiHost"`
	APIKey        string `yaml:"ApiKey"`
	NodeType      string `yaml:"NodeType"`
	NodeID        int64  `yaml:"NodeID"`
	Timeout       int    `yaml:"Timeout"`
	ReportAlive   bool   `yaml:"ReportAlive"`
	AllowInsecure bool   `yaml:"AllowInsecure"`
}

type CertConfig struct {
	CertFile  string `yaml:"CertFile"`
	KeyFile   string `yaml:"KeyFile"`
	SNIPolicy string `yaml:"SniPolicy"`
}

type Node struct {
	Type                        string            `yaml:"Type"`
	PanelTag                    string            `yaml:"PanelTag"`
	NodeType                    string            `yaml:"NodeType"`
	UpdateInterval              Duration          `yaml:"UpdateInterval"`
	APIConfig                   APIConfig         `yaml:"ApiConfig"`
	MultiAPIConfig              []APIConfig       `yaml:"MultiApiConfig"`
	ListenIP                    string            `yaml:"ListenIP"`
	ListenPort                  uint16            `yaml:"ListenPort"`
	DisableTLS                  bool              `yaml:"DisableTLS"`
	ProxyProtocol               bool              `yaml:"ProxyProtocol"`
	AcceptAnyTLS                bool              `yaml:"AcceptAnyTLS"`
	FallbackService             string            `yaml:"FallbackService"`
	ServerPadding               *bool             `yaml:"ServerPadding"`
	PaddingScheme               PaddingScheme     `yaml:"PaddingScheme"`
	AuthenticationTimeout       Duration          `yaml:"AuthenticationTimeout"`
	AuthenticationTimeoutJitter Duration          `yaml:"AuthenticationTimeoutJitter"`
	ALPN                        []string          `yaml:"ALPN"`
	Fallback                    string            `yaml:"Fallback"`
	FallbackForALPN             map[string]string `yaml:"FallbackForALPN"`
	FallbackForServerName       map[string]string `yaml:"FallbackForServerName"`
	CertConfig                  CertConfig        `yaml:"CertConfig"`
	TLSJSON                     string            `yaml:"TLSJson"`
}

type SelectedNode struct {
	Index int
	Node  Node
	API   APIConfig
}

func LoadConfig(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read server config: %w", err)
	}
	var config Config
	if err = yaml.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("decode server config: %w", err)
	}
	if len(config.Nodes) == 0 {
		return nil, fmt.Errorf("server config has no Nodes")
	}
	return &config, nil
}

func (c *Config) AnyTLSNodes() ([]SelectedNode, []string, error) {
	var (
		selected []SelectedNode
		warnings []string
	)
	tags := make(map[string]struct{})
	for index, node := range c.Nodes {
		api := node.APIConfig
		if api.PanelTag == "" {
			api.PanelTag = node.PanelTag
		}
		if api.NodeType == "" {
			api.NodeType = node.NodeType
		}
		if !strings.EqualFold(strings.TrimSpace(api.NodeType), "anytls") {
			warnings = append(warnings, fmt.Sprintf("skip Nodes[%d]: NodeType %q is not AnyTLS", index, api.NodeType))
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(node.Type), "uniproxy") {
			warnings = append(warnings, fmt.Sprintf("skip Nodes[%d]: panel Type %q is not supported for AnyTLS", index, node.Type))
			continue
		}
		if api.APIHost == "" || api.APIKey == "" || api.NodeID <= 0 {
			return nil, warnings, fmt.Errorf("Nodes[%d] requires ApiHost, ApiKey and a positive NodeID", index)
		}
		if api.Timeout <= 0 {
			api.Timeout = 10
		}
		if node.UpdateInterval.Duration == 0 {
			node.UpdateInterval.Duration = defaultUpdateInterval
		}
		if node.UpdateInterval.Duration < 10*time.Second {
			return nil, warnings, fmt.Errorf("Nodes[%d] UpdateInterval must be at least 10s", index)
		}
		if node.ProxyProtocol {
			return nil, warnings, fmt.Errorf("Nodes[%d] ProxyProtocol is not supported by the pure AnyTLS listener", index)
		}
		if node.FallbackService != "" || node.AcceptAnyTLS {
			warnings = append(warnings, fmt.Sprintf("Nodes[%d]: protocol fallback fields are ignored by the pure AnyTLS build", index))
		}
		node.APIConfig = api
		selectedNode := SelectedNode{Index: index, Node: node, API: api}
		if _, loaded := tags[selectedNode.Tag()]; loaded {
			return nil, warnings, fmt.Errorf("Nodes[%d] has duplicate PanelTag %q", index, selectedNode.Tag())
		}
		tags[selectedNode.Tag()] = struct{}{}
		selected = append(selected, selectedNode)
	}
	if len(selected) == 0 {
		return nil, warnings, fmt.Errorf("server config contains no supported AnyTLS UniProxy node")
	}
	if c.IPLimit > 0 || c.RateLimit > 0 || c.ConnectionLimit > 0 {
		warnings = append(warnings, "IPLimit, RateLimit and ConnectionLimit are parsed for compatibility but are not enforced yet")
	}
	return selected, warnings, nil
}

func ResolveConfigPath(configPath string, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), value))
}

func (n SelectedNode) Tag() string {
	tag := strings.TrimSpace(n.API.PanelTag)
	if tag == "" {
		tag = fmt.Sprintf("anytls-%d", n.API.NodeID)
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ">>>", "-")
	return replacer.Replace(tag)
}
