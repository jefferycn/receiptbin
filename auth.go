package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash 用于“用户不存在”时也跑一次 bcrypt，避免用响应时间猜用户名。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)

// ---------- 登录限流 ----------
// 同一用户名 5 次失败锁定 15 分钟。内存计数即可，单机小工具够用。

type loginLimiter struct {
	mu     sync.Mutex
	fails  map[string]int
	locked map[string]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string]int{}, locked: map[string]time.Time{}}
}

func (l *loginLimiter) isLocked(username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.locked[username]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(l.locked, username)
		delete(l.fails, username)
	}
	return false
}

func (l *loginLimiter) recordFailure(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[username]++
	if l.fails[username] >= 5 {
		l.locked[username] = time.Now().Add(15 * time.Minute)
	}
}

func (l *loginLimiter) reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, username)
	delete(l.locked, username)
}

// ---------- 当前用户 ----------

// currentUser 从会话 Cookie 里解析当前用户。
// 网页登录走会话；商品词典 API（/api/products）走独立的 Bearer token，不走这里。
func (a *App) currentUser(r *http.Request) *User {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if u, err := a.store.GetSessionUser(c.Value); err == nil {
			return u
		}
	}
	return nil
}

const sessionCookie = "rb_session"

// setSessionCookie 写登录 Cookie：HttpOnly + SameSite=Lax；Secure 由配置决定
// （反代 HTTPS 时应开启；本地 HTTP 开发时用 SECURE_COOKIES=false）。
func (a *App) setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   sessionDays * 24 * 3600,
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// ---------- 中间件 ----------

// requireAPI 要求 API 请求已登录，否则 401。
// 会话登录的写操作额外做 Origin/Referer 校验，防 CSRF。
func (a *App) requireAPI(next func(w http.ResponseWriter, r *http.Request, u *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := a.currentUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "需要登录")
			return
		}
		if !safeOrigin(r) {
			writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
			return
		}
		next(w, r, u)
	}
}

// requirePage 要求页面请求已登录，未登录重定向到登录页。
func (a *App) requirePage(next func(w http.ResponseWriter, r *http.Request, u *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := a.currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r, u)
	}
}

// safeOrigin：写操作才校验。Origin 或 Referer 存在时，host 必须与本站一致；
// 两者都不存在时视为 curl/脚本等非浏览器请求，放行。
func safeOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		return sameHost(o, r.Host)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return sameHost(ref, r.Host)
	}
	return true
}

func sameHost(rawurl, host string) bool {
	// rawurl 可能是完整 URL；解析失败时按不通过处理
	if strings.HasPrefix(rawurl, "http://") || strings.HasPrefix(rawurl, "https://") {
		rest := rawurl[strings.Index(rawurl, "://")+3:]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		return strings.EqualFold(rest, host)
	}
	return false
}
