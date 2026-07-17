package anytls

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	tlsConfig tls.ServerConfig
	router    adapter.ConnectionRouterEx
	logger    logger.ContextLogger
	listener  *listener.Listener
	service   *anytls.Service

	authenticationTimeout       time.Duration
	authenticationTimeoutJitter time.Duration
	fallbackAddr                M.Socksaddr
	fallbackAddrTLSNextProto    map[string]M.Socksaddr
	fallbackAddrServerName      map[string]M.Socksaddr
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeAnyTLS, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}

	if options.TLS != nil && options.TLS.Enabled {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		inbound.tlsConfig = tlsConfig
	}

	inbound.authenticationTimeout = defaultAuthenticationTimeout
	if options.AuthenticationTimeout > 0 {
		inbound.authenticationTimeout = time.Duration(options.AuthenticationTimeout)
	}
	inbound.authenticationTimeoutJitter = defaultAuthenticationTimeoutJitter
	if options.AuthenticationTimeoutJitter > 0 {
		inbound.authenticationTimeoutJitter = time.Duration(options.AuthenticationTimeoutJitter)
	}

	var fallbackHandler N.TCPConnectionHandlerEx
	if (options.Fallback != nil && options.Fallback.Server != "") || len(options.FallbackForALPN) > 0 || len(options.FallbackForServerName) > 0 {
		if options.Fallback != nil && options.Fallback.Server != "" {
			inbound.fallbackAddr = options.Fallback.Build()
			if !inbound.fallbackAddr.IsValid() {
				return nil, E.New("invalid fallback address: ", inbound.fallbackAddr)
			}
		}
		if len(options.FallbackForALPN) > 0 {
			if inbound.tlsConfig == nil {
				return nil, E.New("fallback for ALPN is not supported without TLS")
			}
			err := validateFallbackALPN([]string(options.TLS.ALPN), options.FallbackForALPN)
			if err != nil {
				return nil, err
			}
			inbound.fallbackAddrTLSNextProto = make(map[string]M.Socksaddr)
			for nextProto, destination := range options.FallbackForALPN {
				fallbackAddr := destination.Build()
				if !fallbackAddr.IsValid() {
					return nil, E.New("invalid fallback address for ALPN ", nextProto, ": ", fallbackAddr)
				}
				inbound.fallbackAddrTLSNextProto[nextProto] = fallbackAddr
			}
		}
		if len(options.FallbackForServerName) > 0 {
			if inbound.tlsConfig == nil {
				return nil, E.New("fallback for server name is not supported without TLS")
			}
			inbound.fallbackAddrServerName = make(map[string]M.Socksaddr)
			for serverName, destination := range options.FallbackForServerName {
				if serverName == "" {
					return nil, E.New("fallback server name is empty")
				}
				if destination == nil {
					return nil, E.New("missing fallback address for server name: ", serverName)
				}
				fallbackAddr := destination.Build()
				if !fallbackAddr.IsValid() {
					return nil, E.New("invalid fallback address for server name ", serverName, ": ", fallbackAddr)
				}
				inbound.fallbackAddrServerName[strings.ToLower(serverName)] = fallbackAddr
			}
		}
		fallbackHandler = adapter.NewUpstreamContextHandler(inbound.fallbackConnection, nil)
	}

	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	serverPadding := true
	if options.ServerPadding != nil {
		serverPadding = *options.ServerPadding
	}
	service, err := anytls.NewService(anytls.ServiceConfig{
		Users: common.Map(options.Users, func(it option.AnyTLSUser) anytls.User {
			return (anytls.User)(it)
		}),
		PaddingScheme:   paddingScheme,
		Handler:         (*inboundHandler)(inbound),
		FallbackHandler: fallbackHandler,
		Logger:          logger,
		ServerPadding:   serverPadding,
	})
	if err != nil {
		return nil, err
	}
	inbound.service = service
	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
	})
	return inbound, nil
}

func validateFallbackALPN(advertisedALPN []string, fallbackForALPN map[string]*option.ServerOptions) error {
	advertised := make(map[string]struct{}, len(advertisedALPN))
	for _, nextProto := range advertisedALPN {
		advertised[nextProto] = struct{}{}
	}
	for nextProto, destination := range fallbackForALPN {
		if nextProto == "" {
			return E.New("fallback ALPN is empty")
		}
		if destination == nil {
			return E.New("missing fallback address for ALPN: ", nextProto)
		}
		if _, loaded := advertised[nextProto]; !loaded {
			return E.New("fallback ALPN is not advertised by TLS: ", nextProto)
		}
	}
	return nil
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	return common.Close(h.listener, h.tlsConfig)
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	conn, disposition, authenticationDeadline, err := prepareConnection(
		conn,
		h.authenticationTimeout,
		h.authenticationTimeoutJitter,
		h.hasFallback(),
	)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": read authentication"))
		return
	}
	if disposition != prefaceAnyTLS {
		fallbackContext := ctx
		if disposition == prefaceFallbackDelayed && !authenticationDeadline.IsZero() {
			fallbackContext = context.WithValue(fallbackContext, authenticationDeadlineContextKey{}, authenticationDeadline)
		}
		h.fallbackConnection(fallbackContext, conn, metadata, onClose)
		return
	}
	serviceContext := adapter.WithContext(ctx, &metadata)
	if !authenticationDeadline.IsZero() {
		serviceContext = context.WithValue(serviceContext, authenticationDeadlineContextKey{}, authenticationDeadline)
	}
	err = h.service.NewConnection(serviceContext, conn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

func (h *Inbound) hasFallback() bool {
	return h.fallbackAddr.IsValid() || len(h.fallbackAddrTLSNextProto) > 0 || len(h.fallbackAddrServerName) > 0
}

func (h *Inbound) fallbackConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if fallbackDelay := authenticationFallbackDelay(ctx, time.Now()); fallbackDelay > 0 {
		timer := time.NewTimer(fallbackDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			N.CloseOnHandshakeFailure(conn, onClose, ctx.Err())
			return
		}
	}

	var fallbackAddr M.Socksaddr
	if len(h.fallbackAddrServerName) > 0 || len(h.fallbackAddrTLSNextProto) > 0 {
		tlsConn, loaded := common.Cast[tls.Conn](conn)
		if !loaded {
			h.logger.DebugContext(ctx, "process connection from ", metadata.Source, ": fallback disabled without TLS state")
			N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
			return
		}
		connectionState := tlsConn.ConnectionState()
		var rejectionReason string
		fallbackAddr, rejectionReason = h.selectFallbackAddress(connectionState.ServerName, connectionState.NegotiatedProtocol)
		if rejectionReason != "" {
			h.logger.DebugContext(ctx, "process connection from ", metadata.Source, ": fallback disabled for ", rejectionReason)
			N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
			return
		}
	} else {
		if !h.fallbackAddr.IsValid() {
			h.logger.DebugContext(ctx, "process connection from ", metadata.Source, ": fallback disabled by default")
			N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
			return
		}
		fallbackAddr = h.fallbackAddr
	}
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	metadata.Destination = fallbackAddr
	h.logger.InfoContext(ctx, "fallback connection to ", fallbackAddr)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) selectFallbackAddress(serverName string, nextProto string) (M.Socksaddr, string) {
	var fallbackAddr M.Socksaddr
	if len(h.fallbackAddrServerName) > 0 {
		var loaded bool
		fallbackAddr, loaded = h.fallbackAddrServerName[strings.ToLower(serverName)]
		if !loaded {
			return M.Socksaddr{}, "server name: " + serverName
		}
	}
	if len(h.fallbackAddrTLSNextProto) > 0 && nextProto != "" {
		var loaded bool
		fallbackAddr, loaded = h.fallbackAddrTLSNextProto[nextProto]
		if !loaded {
			return M.Socksaddr{}, "ALPN: " + nextProto
		}
	}
	if !fallbackAddr.IsValid() {
		if !h.fallbackAddr.IsValid() {
			return M.Socksaddr{}, "default route"
		}
		fallbackAddr = h.fallbackAddr
	}
	return fallbackAddr, ""
}

type authenticationDeadlineContextKey struct{}

func authenticationFallbackDelay(ctx context.Context, now time.Time) time.Duration {
	deadline, loaded := ctx.Value(authenticationDeadlineContextKey{}).(time.Time)
	if !loaded {
		return 0
	}
	delay := deadline.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.Source = source
	metadata.Destination = destination.Unwrap()
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		metadata.User = userName
		h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
