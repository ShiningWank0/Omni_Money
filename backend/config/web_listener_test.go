package config

import "testing"

func TestValidateWebTransport(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WebTransportConfig
		wantErr bool
	}{
		{name: "loopback HTTP", cfg: WebTransportConfig{ListenHost: "127.0.0.1"}},
		{name: "Docker loopback publication", cfg: WebTransportConfig{ListenHost: "0.0.0.0", ExternalHost: "127.0.0.1"}},
		{name: "direct TLS", cfg: WebTransportConfig{ListenHost: "0.0.0.0", TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}},
		{name: "trusted HTTPS proxy", cfg: WebTransportConfig{ListenHost: "0.0.0.0", ForceHTTPS: true, TrustedProxies: "172.20.0.2/32"}},
		{name: "explicit insecure override", cfg: WebTransportConfig{ListenHost: "0.0.0.0", AllowInsecureHTTP: true}},
		{name: "remote HTTP rejected", cfg: WebTransportConfig{ListenHost: "0.0.0.0"}, wantErr: true},
		{name: "external remote overrides internal loopback", cfg: WebTransportConfig{ListenHost: "127.0.0.1", ExternalHost: "192.0.2.10"}, wantErr: true},
		{name: "proxy without trusted source rejected", cfg: WebTransportConfig{ListenHost: "0.0.0.0", ForceHTTPS: true}, wantErr: true},
		{name: "proxy with invalid trusted source rejected", cfg: WebTransportConfig{ListenHost: "0.0.0.0", ForceHTTPS: true, TrustedProxies: "not-an-ip"}, wantErr: true},
		{name: "certificate pair required", cfg: WebTransportConfig{ListenHost: "127.0.0.1", TLSCertFile: "cert.pem"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebTransport(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateWebTransport() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "[::1]"} {
		if !IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = false", host)
		}
	}
	for _, host := range []string{"", "0.0.0.0", "::", "192.0.2.1", "example.com"} {
		if IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = true", host)
		}
	}
}
