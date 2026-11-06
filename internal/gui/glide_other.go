//go:build !darwin

package gui

// glidePanel steps the shown panel to height a frame at a time, keeping it
// by the tray icon as it goes.
func (h *host) glidePanel(height int, g Glide) bool {
	_, from := h.panel.Size()
	gen := h.glides.Add(1)
	go stepGlide(g, from, height, func() bool { return h.glides.Load() == gen }, func(v int) {
		h.panel.SetSize(panelWidth, v)
		_ = h.tray.PositionWindow(h.panel, 6)
	})
	return true
}

// TintPanel: the page paints the panel's tint itself here.
func (h *host) TintPanel(c [4]uint8, ms int) bool { return false }
