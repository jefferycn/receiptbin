// ReceiptBin：自建 Costco 购物记录。
// 小票拍照 → OCR 识别 → 确认入库；按分类统计花费、查商品购买记录、
// 30 天调价提醒、特定商品盯价。Go + SQLite，单二进制部署。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

// App 汇集全部依赖，handler 都挂在它上面。
type App struct {
	store         *Store
	limiter       *loginLimiter
	secureCookies bool
	// apiToken 是商品词典 API 的 Bearer token（RECEIPTBIN_API_TOKEN 环境变量）。
	// 为空时 /api/products 返回 503。只存内存，不写日志。
	apiToken string
}

func main() {
	// 配置全走环境变量，默认值方便本地开发；Docker 里由 compose 传入。
	dataDir := getenv("DATA_DIR", "./data")
	port := getenv("PORT", "8080")
	secureCookies := getenv("SECURE_COOKIES", "true") != "false"

	store, err := openStore(dataDir)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()

	app := &App{store: store, limiter: newLoginLimiter(), secureCookies: secureCookies,
		apiToken: os.Getenv("RECEIPTBIN_API_TOKEN")}
	app.seedIfEmpty()    // 空库时导入示例种子小票
	app.cleanupPending() // 清掉超过 24 小时的未确认上传
	mux := app.routes()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           app.securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// 写超时放宽到 3 分钟：OCR 大图可能跑一分多钟
		WriteTimeout: 3 * time.Minute,
	}
	// 优雅停机：收到 SIGINT/SIGTERM 后等手头请求处理完再退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Printf("ReceiptBin 启动：端口 %s，数据目录 %s", port, dataDir)
	if app.apiToken == "" {
		log.Printf("商品词典 API 未配置 token（RECEIPTBIN_API_TOKEN 为空），/api/products 将返回 503")
	}
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// securityHeaders 给所有响应加安全头。
func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// 不缓存：每次都向服务器确认新版。否则主屏幕 PWA 会一直用旧的
		// CSS/JS，部署了新版手机上还不见效。
		w.Header().Set("Cache-Control", "no-cache")
		// 不允许被嵌进 iframe；脚本/样式只允许本站与内联（内联用于少量页面脚本）
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; "+
				"base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// ---- 页面（需登录的走 requirePage） ----
	mux.HandleFunc("GET /{$}", a.homeHandler)
	mux.HandleFunc("GET /login", servePage("login.html"))
	mux.HandleFunc("GET /register", servePage("register.html"))
	mux.HandleFunc("GET /upload", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("upload.html")(w, r)
	}))
	mux.HandleFunc("GET /receipts", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("receipts.html")(w, r)
	}))
	mux.HandleFunc("GET /receipt/{id}", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("receipt.html")(w, r)
	}))
	mux.HandleFunc("GET /search", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("search.html")(w, r)
	}))
	mux.HandleFunc("GET /item/{itemNo}", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("item.html")(w, r)
	}))
	mux.HandleFunc("GET /alerts", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("alerts.html")(w, r)
	}))
	mux.HandleFunc("GET /watchlist", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("watchlist.html")(w, r)
	}))
	mux.HandleFunc("GET /returns", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("returns.html")(w, r)
	}))
	mux.HandleFunc("GET /settings", a.requirePage(func(w http.ResponseWriter, r *http.Request, u *User) {
		servePage("settings.html")(w, r)
	}))
	mux.Handle("GET /static/", staticFS())

	// ---- API ----
	mux.HandleFunc("GET /api/v1/status", a.statusHandler)
	mux.HandleFunc("POST /api/v1/register", a.registerHandler)
	mux.HandleFunc("POST /api/v1/login", a.loginHandler)
	mux.HandleFunc("POST /api/v1/logout", a.requireAPI(a.logoutHandler))
	mux.HandleFunc("GET /api/v1/me", a.requireAPI(a.meHandler))
	mux.HandleFunc("POST /api/v1/me/password", a.requireAPI(a.changePasswordHandler))

	mux.HandleFunc("GET /api/v1/stats", a.requireAPI(a.statsHandler))

	mux.HandleFunc("POST /api/v1/upload", a.requireAPI(a.uploadHandler))
	mux.HandleFunc("GET /api/v1/pending/{uploadID}", a.requireAPI(a.pendingPhotoHandler))
	mux.HandleFunc("GET /api/v1/receipts", a.requireAPI(a.listReceiptsHandler))
	mux.HandleFunc("POST /api/v1/receipts", a.requireAPI(a.createReceiptHandler))
	mux.HandleFunc("GET /api/v1/receipts/{id}", a.requireAPI(a.getReceiptHandler))
	mux.HandleFunc("DELETE /api/v1/receipts/{id}", a.requireAPI(a.deleteReceiptHandler))
	mux.HandleFunc("GET /api/v1/receipts/{id}/photo", a.requireAPI(a.photoHandler))

	mux.HandleFunc("GET /api/v1/search", a.requireAPI(a.searchHandler))
	mux.HandleFunc("GET /api/v1/items/{itemNo}", a.requireAPI(a.itemDetailHandler))
	mux.HandleFunc("POST /api/v1/observations", a.requireAPI(a.createObservationHandler))

	mux.HandleFunc("GET /api/v1/alerts", a.requireAPI(a.listAlertsHandler))
	mux.HandleFunc("POST /api/v1/alerts/{id}/status", a.requireAPI(a.setAlertStatusHandler))

	mux.HandleFunc("GET /api/v1/returns", a.requireAPI(a.listReturnsHandler))
	mux.HandleFunc("POST /api/v1/returns", a.requireAPI(a.addReturnHandler))
	mux.HandleFunc("POST /api/v1/returns/done-all", a.requireAPI(a.doneAllReturnsHandler))
	mux.HandleFunc("DELETE /api/v1/returns/{id}", a.requireAPI(a.removeReturnHandler))
	mux.HandleFunc("POST /api/v1/returns/{id}/qty", a.requireAPI(a.setReturnQtyHandler))
	mux.HandleFunc("POST /api/v1/returns/{id}/status", a.requireAPI(a.setReturnStatusHandler))

	mux.HandleFunc("GET /api/v1/watchlist", a.requireAPI(a.listWatchlistHandler))
	mux.HandleFunc("POST /api/v1/watchlist", a.requireAPI(a.addWatchHandler))
	mux.HandleFunc("PUT /api/v1/watchlist/{itemNo}", a.requireAPI(a.updateWatchHandler))
	mux.HandleFunc("DELETE /api/v1/watchlist/{itemNo}", a.requireAPI(a.removeWatchHandler))

	// ---- 商品词典 API（v1.2）：机器调用，Bearer token 鉴权，不接受会话 Cookie ----
	mux.HandleFunc("GET /api/products", a.requireAPIToken(a.listProductsHandler))
	mux.HandleFunc("POST /api/products", a.requireAPIToken(a.upsertProductsHandler))
	mux.HandleFunc("DELETE /api/products/{item_number}", a.requireAPIToken(a.deleteProductHandler))
	mux.HandleFunc("GET /api/products/unmapped", a.requireAPIToken(a.listUnmappedHandler))

	// ---- 盯价清单 API（v1.8）：同上，共用 Bearer <redacted> ----
	mux.HandleFunc("GET /api/watchlist", a.requireAPIToken(a.listWatchlistAPIHandler))
	mux.HandleFunc("POST /api/watchlist", a.requireAPIToken(a.addWatchAPIHandler))
	mux.HandleFunc("DELETE /api/watchlist/{itemNo}", a.requireAPIToken(a.removeWatchAPIHandler))

	// ---- 价格观察 API（v1.9）：同上，盯价检查查到的价格经这里写入 ----
	mux.HandleFunc("POST /api/observations", a.requireAPIToken(a.addObservationsAPIHandler))

	// 未匹配的路径：浏览器回首页，API 返回 404
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		writeErr(w, http.StatusNotFound, "不存在")
	})
	return mux
}
