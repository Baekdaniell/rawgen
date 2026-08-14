package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "rawgen",
		Width:            1280,
		Height:           820,
		MinWidth:         1100,
		MinHeight:        720,
		DisableResize:    false,
		Fullscreen:       false,
		Frameless:        false,
		Assets:           assets,
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind:             []interface{}{app},
		BackgroundColour: &options.RGBA{R: 245, G: 247, B: 249, A: 1},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

