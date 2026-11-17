//go:build !server || omni_wails_browser

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

func desktopApplicationOptions() (application.Options, error) {
	options := application.Options{}
	windowsOptions, err := desktopCIWebviewOptions()
	if err != nil {
		return options, err
	}
	if windowsOptions != nil {
		options.Windows = *windowsOptions
	}
	if wailsBrowserMode {
		err := configureWailsBrowserHost(&options)
		return options, err
	}
	middleware, err := newDesktopAssetMiddleware("")
	if err != nil {
		return options, err
	}
	options.Assets.Middleware = middleware
	return options, nil
}
