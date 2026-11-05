package main

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// registerSPA publishes only the client shell and asset files, never the Start server build.
func registerSPA(router *gin.Engine, root string) error {
	shell, err := os.ReadFile(path.Join(root, "_shell.html"))
	if err != nil {
		return fmt.Errorf("load SPA shell (run pnpm run spa:build): %w", err)
	}
	router.HandleMethodNotAllowed = true
	router.NoMethod(func(c *gin.Context) { frontendMiss(c, http.StatusMethodNotAllowed) })
	router.NoRoute(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		p := c.Request.URL.Path
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			frontendMiss(c, http.StatusMethodNotAllowed)
			return
		}
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			frontendMiss(c, http.StatusNotFound)
			return
		}
		if p == "/assets" || strings.HasPrefix(p, "/assets/") {
			name := strings.TrimPrefix(p, "/")
			if p != path.Clean(p) || !publicAsset(p) {
				frontendMiss(c, http.StatusNotFound)
				return
			}
			file, err := os.OpenInRoot(root, name)
			if err != nil {
				frontendMiss(c, http.StatusNotFound)
				return
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				frontendMiss(c, http.StatusNotFound)
				return
			}
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
			return
		}
		if strings.HasPrefix(p, "/.") || strings.HasPrefix(p, "/_") || path.Ext(p) != "" {
			frontendMiss(c, http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Length", strconv.Itoa(len(shell)))
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", shell)
	})
	return nil
}

func publicAsset(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".css", ".svg", ".png", ".jpg", ".jpeg", ".ico", ".webp", ".woff", ".woff2", ".ttf":
		return true
	}
	return false
}

func frontendMiss(c *gin.Context, status int) {
	c.Header("Cache-Control", "no-store")
	if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
		code := "not_found"
		if status == http.StatusMethodNotAllowed {
			code = "method_not_allowed"
		}
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": http.StatusText(status)}})
		return
	}
	c.AbortWithStatus(status)
}
