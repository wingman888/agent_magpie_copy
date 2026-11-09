//go:build !dev

package gui

import (
	"io/fs"
	"net/http"
)

// The shipped app serves the UI from the binary.
func staticFS() fs.FS                     { f, _ := fs.Sub(assets, "assets"); return f }
func devRoutes(*http.ServeMux)            {}
func devListen(http.Handler)              {}
func devPage(h http.Handler) http.Handler { return h }
