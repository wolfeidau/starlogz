package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed pages/index.html public/*
var webFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(webFiles, "pages/index.html"))

type assetManifest struct {
	Script string   `json:"script"`
	Styles []string `json:"styles"`
}

type pageData struct {
	Title string
	assetManifest
}

var manifest = mustLoadManifest()

func mustLoadManifest() assetManifest {
	data, err := webFiles.ReadFile("public/manifest.json")
	if err != nil {
		panic(fmt.Sprintf("dashboard asset manifest missing; run `mise exec -- bun run build`: %v", err))
	}
	var m assetManifest
	if err := json.Unmarshal(data, &m); err != nil || m.Script == "" {
		panic(fmt.Sprintf("dashboard asset manifest invalid; run `mise exec -- bun run build`: %v", err))
	}
	return m
}

func pageHandler(title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_ = pageTemplate.ExecuteTemplate(w, "index.html", pageData{Title: title, assetManifest: manifest})
	}
}

func publicHandler() http.Handler {
	files := http.StripPrefix("/public/", http.FileServer(mustSubFS(webFiles, "public")))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Filenames are content-hashed, so only the manifest that names them must revalidate.
		if r.URL.Path == "/public/manifest.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if _, err := fs.Stat(webFiles, "public/"+strings.TrimPrefix(r.URL.Path, "/public/")); err == nil {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func mustSubFS(fsys embed.FS, dir string) http.FileSystem {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}
