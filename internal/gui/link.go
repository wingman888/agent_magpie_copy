package gui

import (
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/yetone/magpie/internal/provider"
)

// pendingLink is a magpie: link this process was started with.
var pendingLink string

// SetLink queues a magpie: link to be shown for confirmation once the app is up.
func SetLink(link string) { pendingLink = link }

// linkIn finds a magpie: link among command-line arguments.
func linkIn(args []string) string {
	for _, a := range args {
		if provider.IsImportLink(a) {
			return a
		}
	}
	return ""
}

// handleLink shows what a magpie://import link would add and saves it only
// if the user agrees. The key is never displayed in full.
func (h *host) handleLink(link string) {
	if link == "" {
		return
	}
	application.InvokeAsync(func() {
		p, err := provider.ParseImport(link)
		if err != nil {
			h.app.Dialog.Error().SetTitle("magpie").SetMessage("Cannot add this provider: " + err.Error()).Show()
			return
		}
		msg := strings.Join(provider.DescribeImport(p), "\n")
		if provider.Exists(p.ID) {
			msg += "\n\nThis will replace the existing provider " + p.ID + "."
		}
		d := h.app.Dialog.Question().SetTitle("Add provider?").SetMessage(msg)
		add := d.AddButton("Add")
		add.OnClick(func() {
			if err := provider.SaveDraft(p); err != nil {
				h.app.Dialog.Error().SetTitle("magpie").SetMessage(err.Error()).Show()
				return
			}
			h.ShowMain("providers")
		})
		d.AddButton("Cancel")
		d.SetDefaultButton(add)
		d.Show()
	})
}

func (h *host) onSecondLaunch(args []string) { h.handleLink(linkIn(args)) }
