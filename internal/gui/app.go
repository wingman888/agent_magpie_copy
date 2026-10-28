package gui

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

//go:embed tray.png
var trayIcon []byte // black glyph, tinted by the macOS menu bar

//go:embed icon.png
var appIcon []byte // coloured, for other trays and the about box

type host struct {
	app   *application.App
	panel *application.WebviewWindow
	main  *application.WebviewWindow
	tray  *application.SystemTray

	panelHeight int
	query       string // what the windows' URLs carry (a forced theme)

	ready     chan struct{} // closed once the main window can be shown
	readyOnce sync.Once
}

// whenReady runs fn once the main window can be shown safely.
func (h *host) whenReady(fn func()) {
	go func() {
		<-h.ready
		fn()
	}()
}

func (h *host) HidePanel() { h.panel.Hide() }
func (h *host) ShowMain(view string) {
	h.panel.Hide()
	if view != "" {
		h.main.SetURL("/?view=" + view + h.query)
	}
	h.main.Show()
	h.main.Focus()
}

// Import opens the window on an import link, for the user to confirm.
func (h *host) Import(link string) {
	id := stashImport(link)
	h.whenReady(func() {
		h.panel.Hide()
		h.main.SetURL("/?view=providers&import=" + id + h.query)
		h.main.Show()
		h.main.Focus()
	})
}
func (h *host) Quit()              { h.app.Quit() }
func (h *host) OpenURL(url string) { _ = h.app.Browser.OpenURL(url) }

const panelWidth, panelMin, panelMax = 440, 220, 720

// FitPanel grows or shrinks the panel to its content and keeps it anchored
// under the tray icon.
func (h *host) FitPanel(height int) {
	height = max(panelMin, min(panelMax, height))
	if h.panelHeight == height {
		return
	}
	h.panelHeight = height
	h.panel.SetSize(panelWidth, height)
	if h.panel.IsVisible() {
		_ = h.tray.PositionWindow(h.panel, 6)
	}
}

// Run starts the desktop app: a menu bar icon whose click drops down a compact
// panel, plus a regular window for when you want it to stay around.
// showMain opens the window immediately; otherwise only the tray icon appears.
// link is a magpie:// link the app was started with, to confirm and import.
func Run(version string, showMain bool, link string) error {
	// After an update off the Mac, the old process starts this one and then
	// quits; let it go before looking for the gateway.
	update.AwaitPredecessor()
	// The gateway runs inside the app. If another magpie already has the
	// port, that one serves and this one only shows its status.
	var gw *gateway.Server
	if !gateway.Running() {
		gw = gateway.New()
		go func() {
			if err := gw.ListenAndServe(context.Background()); err != nil {
				log.Println("gateway:", err)
			}
		}()
	}
	// Model lists are fetched, never compiled in: whatever the agents can see
	// comes from the models.dev catalog plus each vendor's own /models answer.
	// Keep both halves warm without making the user click anything.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if catalog.Stale() {
			if err := catalog.Sync(ctx); err != nil {
				log.Println("catalog:", err)
			}
		}
		cancel()
		// A signed-in agent's list exists only at the vendor; fill it in the
		// first time so the picker never shows a stale snapshot.
		for _, p := range provider.All() {
			if p.Account == nil || !p.Ready() {
				continue
			}
			if _, ok := p.Fetched(); ok {
				continue
			}
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if _, err := p.Fetch(c); err != nil {
				log.Println(p.ID + ": " + err.Error())
			}
			cancel()
		}
	}()
	go func() {
		if err := registerScheme(); err != nil {
			log.Println("magpie:// links:", err)
		}
	}()
	// MAGPIE_THEME=light|dark forces the palette; handy for screenshots.
	theme := ""
	if t := os.Getenv("MAGPIE_THEME"); t != "" {
		theme = "&theme=" + t
	}
	Version = version
	h := &host{query: theme, ready: make(chan struct{})}
	h.app = application.New(application.Options{
		// Windows and Linux start a new process for a magpie:// link (or a
		// second launch); it hands its arguments to the running one and quits.
		// The Mac sends the link to the running app itself.
		SingleInstance: singleInstance(h),
		Name:           "magpie",
		Description:    "one place to pick every agent's model",
		Icon:           appIcon,
		Assets:         application.AssetOptions{Handler: Handler(h, gw)},
		Mac:            application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Windows:        application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		// A version downloaded but not restarted into is installed on the
		// way out, so the next launch is the new one.
		OnShutdown: func() { updates.install() },
		// Wails exits on some webview errors; say why before it does.
		ErrorHandler: func(err error) { log.Println("magpie:", err) },
	})

	h.panel = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "panel",
		Title:           "magpie",
		URL:             "/?mode=panel" + theme,
		Width:           panelWidth,
		Height:          520,
		Hidden:          true,
		Frameless:       true,
		AlwaysOnTop:     true,
		DisableResize:   true,
		HideOnEscape:    true,
		HideOnFocusLost: true,
		BackgroundType:  application.BackgroundTypeTranslucent,
		Mac: application.MacWindow{
			Backdrop:     application.MacBackdropTranslucent,
			CornerRadius: 12,
		},
		Windows: application.WindowsWindow{HiddenOnTaskbar: true},
	})

	h.main = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "magpie",
		URL:       "/?" + theme,
		Width:     660,
		Height:    600,
		MinWidth:  560,
		MinHeight: 420,
		Hidden:    true,
		Mac: application.MacWindow{
			// no InvisibleTitleBarHeight: that strip drags from anywhere in
			// it, tabs included; the header marks what drags instead
			TitleBar: application.MacTitleBarHiddenInset,
		},
	})
	// Closing the window keeps the tray alive; quitting is a menu action.
	h.main.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		h.main.Hide()
		e.Cancel()
	})

	menu := h.app.NewMenu()
	menu.Add("Open magpie").OnClick(func(*application.Context) { h.ShowMain("") })
	menu.AddSeparator()
	menu.Add("Version " + version).SetEnabled(false)
	restart := menu.Add("Restart to Update").SetHidden(true)
	restart.OnClick(func(*application.Context) {
		if restartToUpdate() {
			h.app.Quit()
		}
	})
	menu.Add("Quit magpie").OnClick(func(*application.Context) { h.app.Quit() })
	updates.onReady = func(v string) {
		application.InvokeSync(func() {
			restart.SetLabel("Restart to Update to " + v).SetHidden(false)
			menu.Update()
		})
	}
	updates.start()

	h.tray = h.app.SystemTray.New()
	h.tray.SetTooltip("magpie")
	if runtime.GOOS == "darwin" {
		h.tray.SetTemplateIcon(trayIcon)
	} else {
		h.tray.SetIcon(appIcon)
	}
	h.tray.SetMenu(menu)
	h.tray.AttachWindow(h.panel).WindowOffset(6)
	// the quick panel by the icon, or the main window if the user would
	// rather (Settings → Tray icon)
	h.tray.OnClick(func() {
		if settings.Load().Tray == "window" {
			h.ShowMain("")
			return
		}
		h.tray.ToggleWindow()
	})

	// Wails shows a Windows webview 3s after Show whether or not WebView2
	// has made its controller yet, and a slow first start then crashes on
	// the nil controller; there, wait for the first page.
	markReady := func() { h.readyOnce.Do(func() { close(h.ready) }) }
	if runtime.GOOS == "windows" {
		h.main.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) { markReady() })
	} else {
		h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { markReady() })
	}
	if showMain {
		h.whenReady(func() { h.ShowMain("") })
	}
	if link != "" {
		h.Import(link)
	}
	// Windows and Linux also report the start's own link as an event.
	var skip sync.Once
	h.app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		u := ImportLink([]string{e.Context().URL()})
		dup := false
		if u == link {
			skip.Do(func() { dup = true })
		}
		if u != "" && !dup {
			h.Import(u)
		}
	})
	return h.app.Run()
}

// singleInstance makes a second launch hand over to this one, off the Mac.
// The id covers the executable and the config dir, so a build elsewhere or
// a sandboxed HOME runs on its own.
func singleInstance(h *host) *application.SingleInstanceOptions {
	if runtime.GOOS == "darwin" || !sessionBus() {
		return nil
	}
	exe, _ := os.Executable()
	sum := sha256.Sum256([]byte(exe + "\x00" + settings.Dir()))
	return &application.SingleInstanceOptions{
		UniqueID: "ai.usemagpie.app.i" + hex.EncodeToString(sum[:6]),
		OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
			args := d.Args
			if len(args) > 0 {
				args = args[1:]
			}
			switch {
			case ImportLink(args) != "":
				h.Import(ImportLink(args))
			case len(args) == 1 && args[0] == "tray":
			default:
				h.whenReady(func() { h.ShowMain("") })
			}
		},
	}
}
