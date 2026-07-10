package server

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed pages/index.html public/*
var webFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(webFiles, "pages/index.html"))

type pageData struct {
	Title string
}

func pageHandler(title string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pageTemplate.ExecuteTemplate(w, "index.html", pageData{Title: title})
	}
}

func publicHandler() http.Handler {
	return http.StripPrefix("/public/", http.FileServer(mustSubFS(webFiles, "public")))
}

func mustSubFS(fsys embed.FS, dir string) http.FileSystem {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}
