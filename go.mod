module omni_money

// Go 1.26.11 includes the net/http fix for GO-2026-6859 and the cgo
// security fixes, in addition to the earlier standard-library fixes.
// CI and release builds use the pinned toolchain below.
go 1.26.11

toolchain go1.27.4

require (
	github.com/fxamacker/cbor/v2 v2.10.0
	github.com/go-webauthn/webauthn v0.18.2
	github.com/labstack/echo/v4 v4.16.4
	github.com/mattn/go-sqlite3 v1.14.53
	github.com/wailsapp/wails/v3 v3.1.0
	golang.org/x/crypto v0.59.0
	golang.org/x/image v0.48.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/coder/websocket v1.8.14 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/labstack/gommon v0.5.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/term v0.48.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
