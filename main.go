package main

import (
	"embed"
	"fmt"
	"os"

	"lokalkit/internal/core"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Elevated helper mode: "lokalkit.exe --sync-hosts <root>" is started
	// with admin rights (UAC) only to update the Windows hosts file.
	if len(os.Args) >= 3 && os.Args[1] == "--sync-hosts" {
		if err := core.RunElevatedHostsSync(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	app := NewApp()
	err := wails.Run(&options.App{
		Title: "LokalKit",
		// Fixed size that fits the layout (1120px content + margins);
		// resizing and maximise are turned off on purpose.
		Width:         1184,
		Height:        760,
		DisableResize: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "my.codeslab.lokalkit",
			// Opening LokalKit again while it sits in the tray shows the window.
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.ShowWindow() },
		},
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
