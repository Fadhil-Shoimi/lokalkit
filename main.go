package main

import (
	"strconv"
	"strings"
	"time"

	"embed"
	"fmt"
	"lokalkit/internal/updater"
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
	// After an update the new LokalKit is started with
	// "--wait-pid <old pid> --resume apache,mysql": wait for the old one to
	// exit, then start the services that were running.
	for i := 1; i < len(os.Args)-1; i++ {
		switch os.Args[i] {
		case "--wait-pid":
			if pid, err := strconv.Atoi(os.Args[i+1]); err == nil {
				updater.WaitForExit(pid, 30*time.Second)
			}
		case "--resume":
			app.resume = strings.Split(os.Args[i+1], ",")
		}
	}
	err := wails.Run(&options.App{
		Title: "LokalKit",
		// Opens at a size that fits the one-column layout; wider windows
		// (or maximised) get the side Activity panel and two-column lists.
		Width:     1184,
		Height:    760,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Matches the splash so the window never flashes white while loading.
		BackgroundColour: &options.RGBA{R: 21, G: 23, B: 28, A: 1},
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
