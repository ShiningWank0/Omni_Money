//go:build !omni_wails_browser

package main

import (
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func configureWailsBrowserHost(_ *application.Options) error {
	return errors.New("browser diagnostic host is excluded from this binary")
}
