package panelcompat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sagernet/sing-box/log"
)

const reportInterval = time.Minute

type RunOptions struct {
	Context          context.Context
	BaseConfigPath   string
	ServerConfigPath string
	StateDirectory   string
	Executable       string
	CheckOnly        bool
}

type RunResult struct {
	RuntimePath string
	NodeCount   int
	UserCount   int
}

type cacheFile struct {
	States map[int]*NodeState `json:"states"`
}

type supervisor struct {
	options        RunOptions
	config         *Config
	selected       []SelectedNode
	clients        map[int]*APIClient
	states         map[int]*NodeState
	nextPoll       map[int]time.Time
	internal       InternalAPI
	runtimeIndex   RuntimeIndex
	runtimePath    string
	cachePath      string
	child          *exec.Cmd
	childExit      chan error
	pendingTraffic map[int]map[int64][2]int64
}

func Run(options RunOptions) (RunResult, error) {
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.BaseConfigPath == "" {
		return RunResult{}, fmt.Errorf("base config path is required")
	}
	if options.ServerConfigPath == "" {
		return RunResult{}, fmt.Errorf("server config path is required")
	}
	if options.StateDirectory == "" {
		options.StateDirectory = filepath.Join(filepath.Dir(options.ServerConfigPath), ".panel-state")
	}
	if options.Executable == "" {
		executable, err := os.Executable()
		if err != nil {
			return RunResult{}, err
		}
		options.Executable = executable
	}
	config, err := LoadConfig(options.ServerConfigPath)
	if err != nil {
		return RunResult{}, err
	}
	selected, warnings, err := config.AnyTLSNodes()
	for _, warning := range warnings {
		log.Warn(warning)
	}
	if err != nil {
		return RunResult{}, err
	}
	if !options.CheckOnly && runtime.GOOS == "windows" {
		return RunResult{}, fmt.Errorf("server mode is supported on Linux and other Unix systems; use --check on Windows")
	}
	if err = os.MkdirAll(options.StateDirectory, 0o700); err != nil {
		return RunResult{}, fmt.Errorf("create state directory: %w", err)
	}
	statsAddress, err := reserveLoopbackAddress()
	if err != nil {
		return RunResult{}, err
	}
	clashAddress, err := reserveLoopbackAddress(statsAddress)
	if err != nil {
		return RunResult{}, err
	}
	clashSecret, err := randomSecret()
	if err != nil {
		return RunResult{}, err
	}
	manager := &supervisor{
		options:  options,
		config:   config,
		selected: selected,
		clients:  make(map[int]*APIClient),
		states:   make(map[int]*NodeState),
		nextPoll: make(map[int]time.Time),
		internal: InternalAPI{
			StatsAddress: statsAddress,
			ClashAddress: clashAddress,
			ClashSecret:  clashSecret,
		},
		runtimePath:    RuntimePath(options.StateDirectory),
		cachePath:      filepath.Join(options.StateDirectory, "panel-cache.json"),
		pendingTraffic: make(map[int]map[int64][2]int64),
	}
	manager.loadCache()
	for _, node := range selected {
		manager.clients[node.Index] = NewAPIClient(node)
	}
	if _, err = manager.syncNodes(options.Context, selected, true); err != nil {
		return RunResult{}, err
	}
	if err = manager.render(options.Context); err != nil {
		return RunResult{}, err
	}
	result := RunResult{
		RuntimePath: manager.runtimePath,
		NodeCount:   len(selected),
		UserCount:   len(manager.runtimeIndex.Users),
	}
	if options.CheckOnly {
		return result, nil
	}
	if err = manager.startChild(); err != nil {
		return RunResult{}, err
	}
	err = manager.loop(options.Context)
	return result, err
}

func (s *supervisor) loop(ctx context.Context) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, processSignals()...)
	defer signal.Stop(signals)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextReport := time.Now().Add(reportInterval)
	for {
		select {
		case <-ctx.Done():
			return s.stopChild()
		case signalValue := <-signals:
			if !isReloadSignal(signalValue) {
				return s.stopChild()
			}
			if err := s.collectAndReport(ctx); err != nil {
				log.Warn("panel report before reload: ", err)
			}
			_, err := s.syncNodes(ctx, s.selected, false)
			if err != nil {
				log.Error("panel refresh: ", err)
			}
			if err = s.renderAndReload(ctx); err != nil {
				return err
			}
		case err := <-s.childExit:
			if err == nil {
				return fmt.Errorf("sing-box child exited unexpectedly")
			}
			return fmt.Errorf("sing-box child exited: %w", err)
		case now := <-ticker.C:
			if !now.Before(nextReport) {
				if err := s.collectAndReport(ctx); err != nil {
					log.Warn("panel report: ", err)
				}
				nextReport = now.Add(reportInterval)
			}
			var due []SelectedNode
			for _, node := range s.selected {
				if !now.Before(s.nextPoll[node.Index]) {
					due = append(due, node)
				}
			}
			if len(due) == 0 {
				continue
			}
			changed, err := s.syncNodes(ctx, due, false)
			if err != nil {
				log.Warn("panel refresh: ", err)
			}
			if changed {
				if err = s.collectAndReport(ctx); err != nil {
					log.Warn("panel report before config reload: ", err)
				}
				if err = s.renderAndReload(ctx); err != nil {
					return err
				}
			}
		}
	}
}

func (s *supervisor) syncNodes(ctx context.Context, nodes []SelectedNode, startup bool) (bool, error) {
	changed := false
	var failures []error
	now := time.Now()
	for _, node := range nodes {
		previous := s.states[node.Index]
		state, nodeChanged, err := s.clients[node.Index].Fetch(ctx, previous)
		s.nextPoll[node.Index] = now.Add(node.Node.UpdateInterval.Duration)
		if err != nil {
			if startup && previous != nil {
				log.Warn("use cached panel state for ", node.Tag(), ": ", err)
				continue
			}
			failures = append(failures, err)
			continue
		}
		s.states[node.Index] = state
		changed = changed || nodeChanged || previous == nil
	}
	if startup {
		for _, node := range s.selected {
			if s.states[node.Index] == nil {
				return false, fmt.Errorf("no live or cached panel state for %s", node.Tag())
			}
		}
	}
	if changed {
		if err := s.saveCache(); err != nil {
			return false, err
		}
	}
	return changed, errors.Join(failures...)
}

func (s *supervisor) render(ctx context.Context) error {
	baseContent, err := os.ReadFile(s.options.BaseConfigPath)
	if err != nil {
		return fmt.Errorf("read base config: %w", err)
	}
	content, index, err := BuildRuntimeConfig(
		ctx,
		baseContent,
		s.options.ServerConfigPath,
		s.selected,
		s.states,
		s.internal,
	)
	if err != nil {
		return err
	}
	if err = writeFileAtomic(s.runtimePath, content, 0o600); err != nil {
		return fmt.Errorf("write runtime config: %w", err)
	}
	s.runtimeIndex = index
	return nil
}

func (s *supervisor) renderAndReload(ctx context.Context) error {
	if err := s.render(ctx); err != nil {
		return err
	}
	if err := reloadProcess(s.child.Process); err != nil {
		return fmt.Errorf("reload sing-box child: %w", err)
	}
	log.Info("reloaded ", len(s.selected), " AnyTLS node(s), ", len(s.runtimeIndex.Users), " user(s)")
	return nil
}

func (s *supervisor) startChild() error {
	command := exec.Command(s.options.Executable, "run", "-c", s.runtimePath, "--disable-color")
	command.Dir = filepath.Dir(s.options.BaseConfigPath)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start sing-box child: %w", err)
	}
	s.child = command
	s.childExit = make(chan error, 1)
	go func() {
		s.childExit <- command.Wait()
	}()
	log.Info("started ", len(s.selected), " AnyTLS node(s), ", len(s.runtimeIndex.Users), " user(s)")
	return nil
}

func (s *supervisor) stopChild() error {
	if s.child == nil || s.child.Process == nil {
		return nil
	}
	_ = terminateProcess(s.child.Process)
	select {
	case err := <-s.childExit:
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				return err
			}
		}
		return nil
	case <-time.After(10 * time.Second):
		_ = s.child.Process.Kill()
		<-s.childExit
		return nil
	}
}

func (s *supervisor) collectAndReport(ctx context.Context) error {
	traffic, err := collectTraffic(ctx, s.internal.StatsAddress, s.runtimeIndex)
	if err != nil {
		return err
	}
	mergeTraffic(s.pendingTraffic, traffic)
	var failures []error
	for _, node := range s.selected {
		nodeTraffic := s.pendingTraffic[node.Index]
		if len(nodeTraffic) == 0 {
			continue
		}
		if err = s.clients[node.Index].PushTraffic(ctx, nodeTraffic); err != nil {
			failures = append(failures, fmt.Errorf("%s traffic: %w", node.Tag(), err))
			continue
		}
		delete(s.pendingTraffic, node.Index)
	}
	if s.hasAliveReporting() {
		alive, aliveErr := collectAlive(ctx, s.internal.ClashAddress, s.internal.ClashSecret, s.runtimeIndex)
		if aliveErr != nil {
			failures = append(failures, aliveErr)
		} else {
			for _, node := range s.selected {
				if !node.API.ReportAlive {
					continue
				}
				if err = s.clients[node.Index].PushAlive(ctx, alive[node.Index]); err != nil {
					failures = append(failures, fmt.Errorf("%s alive: %w", node.Tag(), err))
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (s *supervisor) hasAliveReporting() bool {
	for _, node := range s.selected {
		if node.API.ReportAlive {
			return true
		}
	}
	return false
}

func (s *supervisor) loadCache() {
	content, err := os.ReadFile(s.cachePath)
	if err != nil {
		return
	}
	var cache cacheFile
	if json.Unmarshal(content, &cache) == nil && cache.States != nil {
		s.states = cache.States
	}
}

func (s *supervisor) saveCache() error {
	content, err := json.MarshalIndent(cacheFile{States: s.states}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.cachePath, append(content, '\n'), 0o600)
}

func reserveLoopbackAddress(excluded ...string) (string, error) {
	for range 10 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		address := listener.Addr().String()
		if err = listener.Close(); err != nil {
			return "", err
		}
		collision := false
		for _, excludedAddress := range excluded {
			collision = collision || address == excludedAddress
		}
		if !collision {
			return address, nil
		}
	}
	return "", fmt.Errorf("failed to allocate distinct loopback addresses")
}

func randomSecret() (string, error) {
	content := make([]byte, 24)
	if _, err := rand.Read(content); err != nil {
		return "", err
	}
	return hex.EncodeToString(content), nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(content)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	return os.Rename(tempPath, path)
}
