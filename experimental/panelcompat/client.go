package panelcompat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type User struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int64  `json:"speed_limit"`
	DeviceLimit int64  `json:"device_limit"`
}

type NodeState struct {
	Index      int            `json:"index"`
	Config     map[string]any `json:"config"`
	Users      []User         `json:"users"`
	ConfigETag string         `json:"config_etag,omitempty"`
	UsersETag  string         `json:"users_etag,omitempty"`
}

type APIClient struct {
	node   SelectedNode
	client *http.Client
}

func NewAPIClient(node SelectedNode) *APIClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if node.API.AllowInsecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &APIClient{
		node: node,
		client: &http.Client{
			Timeout:   time.Duration(node.API.Timeout) * time.Second,
			Transport: transport,
		},
	}
}

func (c *APIClient) Fetch(ctx context.Context, previous *NodeState) (*NodeState, bool, error) {
	state := &NodeState{Index: c.node.Index}
	if previous != nil {
		*state = *previous
		state.Config = cloneMap(previous.Config)
		state.Users = append([]User(nil), previous.Users...)
	}
	var configResponse map[string]any
	configChanged, err := c.getJSON(ctx, "config", state.ConfigETag, &configResponse, &state.ConfigETag)
	if err != nil {
		return nil, false, fmt.Errorf("%s config: %w", c.node.Tag(), err)
	}
	if configChanged {
		if data, loaded := configResponse["data"].(map[string]any); loaded && stringValue(configResponse["protocol"]) == "" {
			state.Config = data
		} else {
			state.Config = configResponse
		}
	}
	var usersResponse struct {
		Users []User `json:"users"`
		Data  *struct {
			Users []User `json:"users"`
		} `json:"data,omitempty"`
	}
	usersChanged, err := c.getJSON(ctx, "user", state.UsersETag, &usersResponse, &state.UsersETag)
	if err != nil {
		return nil, false, fmt.Errorf("%s users: %w", c.node.Tag(), err)
	}
	if usersChanged {
		if usersResponse.Data != nil {
			state.Users = usersResponse.Data.Users
		} else {
			state.Users = usersResponse.Users
		}
		for _, user := range state.Users {
			if user.ID <= 0 || strings.TrimSpace(user.UUID) == "" {
				return nil, false, fmt.Errorf("%s returned an invalid user", c.node.Tag())
			}
		}
	}
	if state.Config == nil {
		return nil, false, fmt.Errorf("%s returned an empty node config", c.node.Tag())
	}
	return state, configChanged || usersChanged, nil
}

func (c *APIClient) PushTraffic(ctx context.Context, traffic map[int64][2]int64) error {
	if len(traffic) == 0 {
		return nil
	}
	body := make(map[string][2]int64, len(traffic))
	for userID, value := range traffic {
		body[fmt.Sprint(userID)] = value
	}
	return c.postJSON(ctx, "push", body)
}

func (c *APIClient) PushAlive(ctx context.Context, alive map[int64][]string) error {
	body := make(map[string][]string, len(alive))
	for userID, addresses := range alive {
		body[fmt.Sprint(userID)] = addresses
	}
	return c.postJSON(ctx, "alive", body)
}

func (c *APIClient) getJSON(ctx context.Context, operation string, etag string, destination any, nextETag *string) (bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, operation, nil)
	if err != nil {
		return false, err
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, responseError(response)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	decoder.UseNumber()
	if err = decoder.Decode(destination); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	if value := response.Header.Get("ETag"); value != "" {
		*nextETag = value
	}
	return true, nil
}

func (c *APIClient) postJSON(ctx context.Context, operation string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, http.MethodPost, operation, bytes.NewReader(content))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	return nil
}

func (c *APIClient) newRequest(ctx context.Context, method string, operation string, body io.Reader) (*http.Request, error) {
	endpoint, err := buildEndpoint(c.node.API.APIHost, operation)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("token", c.node.API.APIKey)
	query.Set("node_id", fmt.Sprint(c.node.API.NodeID))
	query.Set("node_type", "anytls")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sing-box-anytls-panel/1")
	return request, nil
}

func buildEndpoint(apiHost string, operation string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(apiHost))
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("ApiHost must use http or https")
	}
	cleanPath := strings.TrimSuffix(endpoint.Path, "/")
	lowerPath := strings.ToLower(cleanPath)
	switch {
	case strings.HasSuffix(lowerPath, "/api/v1/server/uniproxy"):
		endpoint.Path = path.Join(cleanPath, operation)
	case strings.HasSuffix(lowerPath, "/api/v1"):
		endpoint.Path = path.Join(cleanPath, "server/UniProxy", operation)
	default:
		endpoint.Path = path.Join(cleanPath, "api/v1/server/UniProxy", operation)
	}
	if !strings.HasPrefix(endpoint.Path, "/") {
		endpoint.Path = "/" + endpoint.Path
	}
	return endpoint, nil
}

func responseError(response *http.Response) error {
	content, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	message := strings.TrimSpace(string(content))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("HTTP %d: %s", response.StatusCode, message)
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	content, _ := json.Marshal(source)
	var destination map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	_ = decoder.Decode(&destination)
	return destination
}
