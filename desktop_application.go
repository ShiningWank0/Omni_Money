//go:build !server || omni_wails_browser

package main

import (
	"errors"
	"os"
	"strconv"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func desktopApplicationOptions() (application.Options, error) {
	options := application.Options{}
	origin := ""
	if wailsBrowserMode {
		// This headless binding host is a CI diagnostic, never the product server.
		if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("WAILS_SERVER_HOST") != "127.0.0.1" {
			return options, errors.New("Wails browser diagnostics require an isolated CI loopback host")
		}
		port, err := strconv.Atoi(os.Getenv("WAILS_SERVER_PORT"))
		if err != nil || port < 1 || port > 65535 {
			return options, errors.New("invalid CI Wails browser port")
		}
		options.Server.Host = "127.0.0.1"
		options.Server.Port = port
		origin = "http://127.0.0.1:" + strconv.Itoa(port)
	}
	middleware, err := newDesktopAssetMiddleware(origin)
	if err != nil {
		return options, err
	}
	options.Assets.Middleware = middleware
	return options, nil
}
