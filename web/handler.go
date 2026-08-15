// Package web serves the production React dashboard embedded in the xform binary.
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:generate npm ci
//go:generate npm run build

//go:embed all:dist
var dashboardFiles embed.FS

// Handler returns an HTTP handler for the embedded dashboard and its assets.
func Handler() http.Handler {
	assets, err := fs.Sub(dashboardFiles, "dist")
	if err != nil {
		panic("open embedded dashboard: " + err.Error())
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic("read embedded dashboard index: " + err.Error())
	}

	return &dashboardHandler{
		assets: assets,
		files:  http.FileServer(http.FS(assets)),
		index:  index,
	}
}

type dashboardHandler struct {
	assets fs.FS
	files  http.Handler
	index  []byte
}

func (handler *dashboardHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	assetPath := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if assetPath == "." {
		assetPath = ""
	}
	if assetPath != "" {
		info, err := fs.Stat(handler.assets, assetPath)
		if err == nil && !info.IsDir() {
			if strings.HasPrefix(assetPath, "assets/") {
				response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			handler.files.ServeHTTP(response, request)
			return
		}
		if err == nil || path.Ext(assetPath) != "" {
			http.NotFound(response, request)
			return
		}
	}

	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(response, request, "index.html", time.Time{}, bytes.NewReader(handler.index))
}
