package panelcompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sagernet/sing-box/experimental/v2rayapi"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func collectTraffic(ctx context.Context, address string, index RuntimeIndex) (map[int]map[int64][2]int64, error) {
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	response, err := v2rayapi.NewStatsServiceClient(connection).QueryStats(requestContext, &v2rayapi.QueryStatsRequest{
		Patterns: []string{`^user>>>.*>>>traffic>>>(uplink|downlink)$`},
		Regexp:   true,
		Reset_:   true,
	})
	if err != nil {
		return nil, err
	}
	result := make(map[int]map[int64][2]int64)
	for _, stat := range response.Stat {
		label, direction, loaded := parseUserStatName(stat.Name)
		if !loaded || stat.Value == 0 {
			continue
		}
		binding, loaded := index.Users[label]
		if !loaded {
			continue
		}
		nodeTraffic := result[binding.NodeIndex]
		if nodeTraffic == nil {
			nodeTraffic = make(map[int64][2]int64)
			result[binding.NodeIndex] = nodeTraffic
		}
		value := nodeTraffic[binding.UserID]
		if direction == "uplink" {
			value[0] += stat.Value
		} else {
			value[1] += stat.Value
		}
		nodeTraffic[binding.UserID] = value
	}
	return result, nil
}

func parseUserStatName(name string) (label string, direction string, loaded bool) {
	const prefix = "user>>>"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	body := strings.TrimPrefix(name, prefix)
	separator := strings.LastIndex(body, ">>>traffic>>>")
	if separator <= 0 {
		return "", "", false
	}
	label = body[:separator]
	direction = body[separator+len(">>>traffic>>>"):]
	if direction != "uplink" && direction != "downlink" {
		return "", "", false
	}
	return label, direction, true
}

func mergeTraffic(destination map[int]map[int64][2]int64, source map[int]map[int64][2]int64) {
	for nodeIndex, users := range source {
		nodeTraffic := destination[nodeIndex]
		if nodeTraffic == nil {
			nodeTraffic = make(map[int64][2]int64)
			destination[nodeIndex] = nodeTraffic
		}
		for userID, current := range users {
			value := nodeTraffic[userID]
			value[0] += current[0]
			value[1] += current[1]
			nodeTraffic[userID] = value
		}
	}
}

func collectAlive(ctx context.Context, address string, secret string, index RuntimeIndex) (map[int]map[int64][]string, error) {
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, "http://"+address+"/connections", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return nil, fmt.Errorf("clash API HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(content)))
	}
	var snapshot struct {
		Connections []struct {
			Metadata struct {
				SourceIP    string `json:"sourceIP"`
				InboundUser string `json:"inboundUser"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&snapshot); err != nil {
		return nil, err
	}
	sets := make(map[int]map[int64]map[string]struct{})
	for _, connection := range snapshot.Connections {
		binding, loaded := index.Users[connection.Metadata.InboundUser]
		if !loaded || connection.Metadata.SourceIP == "" {
			continue
		}
		nodeSet := sets[binding.NodeIndex]
		if nodeSet == nil {
			nodeSet = make(map[int64]map[string]struct{})
			sets[binding.NodeIndex] = nodeSet
		}
		userSet := nodeSet[binding.UserID]
		if userSet == nil {
			userSet = make(map[string]struct{})
			nodeSet[binding.UserID] = userSet
		}
		userSet[connection.Metadata.SourceIP] = struct{}{}
	}
	result := make(map[int]map[int64][]string)
	for nodeIndex, users := range sets {
		nodeAlive := make(map[int64][]string, len(users))
		for userID, addresses := range users {
			values := make([]string, 0, len(addresses))
			for address := range addresses {
				values = append(values, address)
			}
			sort.Strings(values)
			nodeAlive[userID] = values
		}
		result[nodeIndex] = nodeAlive
	}
	return result, nil
}
