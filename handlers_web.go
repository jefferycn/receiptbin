package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed web
var webFS embed.FS

// servePage 发送 web/pages 下的静态 HTML（登录后页面，内容由前端 JS 调 API 填充）。
func servePage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := webFS.ReadFile("web/pages/" + name)
		if err != nil {
			http.Error(w, "页面不存在", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
}

// staticFS 是 /static/ 的文件源（CSS/JS/图标），从 embed 里取。
func staticFS() http.Handler {
	sub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// homeHandler 处理 GET /：未登录去登录页，已登录进仪表盘。
func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	servePage("index.html")(w, r)
}

// cleanupPending 清掉超过 24 小时的未确认上传照片（启动时跑一次）。
func (a *App) cleanupPending() {
	dir := filepath.Join(a.store.dataDir, "pending")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
