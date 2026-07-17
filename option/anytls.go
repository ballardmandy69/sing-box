package option

import "github.com/sagernet/sing/common/json/badoption"

type AnyTLSInboundOptions struct {
	ListenOptions
	InboundTLSOptionsContainer
	Users                       []AnyTLSUser               `json:"users,omitempty"`
	PaddingScheme               badoption.Listable[string] `json:"padding_scheme,omitempty"`
	ServerPadding               *bool                      `json:"server_padding,omitempty"`
	AuthenticationTimeout       badoption.Duration         `json:"authentication_timeout,omitempty"`
	AuthenticationTimeoutJitter badoption.Duration         `json:"authentication_timeout_jitter,omitempty"`
	Fallback                    *ServerOptions             `json:"fallback,omitempty"`
	FallbackForALPN             map[string]*ServerOptions  `json:"fallback_for_alpn,omitempty"`
	FallbackForServerName       map[string]*ServerOptions  `json:"fallback_for_server_name,omitempty"`
}

type AnyTLSUser struct {
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
}

type AnyTLSOutboundOptions struct {
	DialerOptions
	ServerOptions
	OutboundTLSOptionsContainer
	Password                 string             `json:"password,omitempty"`
	IdleSessionCheckInterval badoption.Duration `json:"idle_session_check_interval,omitempty"`
	IdleSessionTimeout       badoption.Duration `json:"idle_session_timeout,omitempty"`
	MinIdleSession           int                `json:"min_idle_session,omitempty"`
}
