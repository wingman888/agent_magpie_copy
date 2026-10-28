package gui

import (
	"context"
	_ "embed"
	"log"
	"os"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/dial/internal/gateway"
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
}

func (h *host) HidePanel() { h.panel.Hide() }
func (h *host) ShowMain() {
	h.panel.Hide()
	h.main.Show()
	h.main.Focus()
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
func Run(version string, showMain bool) error {
	// The gateway runs inside the app. If another dial already has the
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
	// DIAL_THEME=light|dark forces the palette; handy for screenshots.
	theme := ""
	if t := os.Getenv("DIAL_THEME"); t != "" {
		theme = "&theme=" + t
	}
	h := &host{}
	h.app = application.New(application.Options{
		Name:        "dial",
		Description: "one dial for every coding agent's model",
		Icon:        appIcon,
		Assets:      application.AssetOptions{Handler: Handler(h, gw)},
		Mac:         application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Windows:     application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
	})

	h.panel = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "panel",
		Title:           "dial",
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
		Title:     "dial",
		URL:       "/?" + theme,
		Width:     660,
		Height:    600,
		MinWidth:  560,
		MinHeight: 420,
		Hidden:    true,
		Mac: application.MacWindow{
			TitleBar:                application.MacTitleBarHiddenInset,
			InvisibleTitleBarHeight: 44,
		},
	})
	// Closing the window keeps the tray alive; quitting is a menu action.
	h.main.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		h.main.Hide()
		e.Cancel()
	})

	menu := h.app.NewMenu()
	menu.Add("Open dial").OnClick(func(*application.Context) { h.ShowMain() })
	menu.AddSeparator()
	menu.Add("Version " + version).SetEnabled(false)
	menu.Add("Quit dial").OnClick(func(*application.Context) { h.app.Quit() })

	h.tray = h.app.SystemTray.New()
	h.tray.SetTooltip("dial")
	if runtime.GOOS == "darwin" {
		h.tray.SetTemplateIcon(trayIcon)
	} else {
		h.tray.SetIcon(appIcon)
	}
	h.tray.SetMenu(menu)
	h.tray.AttachWindow(h.panel).WindowOffset(6)

	if showMain {
		h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { h.ShowMain() })
	}
	return h.app.Run()
}
