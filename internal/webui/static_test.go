package webui

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func buildFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"index.html":              "<!doctype html><title>DanmakuFlow</title>",
		"assets/app-abc123.js":    "console.log('room')",
		"assets/app-abc123.css":   "body { color: blue; }",
		"assets/nested/font.woff": "font-data",
		"private.txt":             "not-public",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func routerWithUI(t *testing.T, dir string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	if err := Register(router, dir); err != nil {
		t.Fatal(err)
	}
	return router
}

func TestServesBuildFilesWithoutReplacingWebSocketRoute(t *testing.T) {
	router := routerWithUI(t, buildFixture(t))
	for _, test := range []struct {
		path, content, contentType string
		status                     int
	}{
		{"/", "<!doctype html><title>DanmakuFlow</title>", "text/html", http.StatusOK},
		{"/assets/app-abc123.js", "console.log('room')", "javascript", http.StatusOK},
		{"/assets/app-abc123.css", "body { color: blue; }", "text/css", http.StatusOK},
		{"/assets/nested/font.woff", "font-data", "", http.StatusOK},
		{"/ws", "", "", http.StatusNoContent},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status || response.Body.String() != test.content {
				t.Fatalf("GET %s = %d %q", test.path, response.Code, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("GET %s content type = %q", test.path, response.Header().Get("Content-Type"))
			}
			if test.path == "/" && response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("index must be revalidated after a new frontend build")
			}
		})
	}
}

func TestMissingBuildLeavesExistingRoutesAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, dir := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		router := gin.New()
		router.GET("/ws", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		if err := Register(router, dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing build error = %v", err)
		}
		for path, status := range map[string]int{"/": http.StatusNotFound, "/ws": http.StatusNoContent} {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != status {
				t.Fatalf("GET %s = %d, want %d", path, response.Code, status)
			}
		}
	}
}

func TestRejectsDirectoriesUnknownFilesAndTraversal(t *testing.T) {
	router := routerWithUI(t, buildFixture(t))
	for _, path := range []string{
		"/missing", "/index.html", "/private.txt", "/.env", "/assets/", "/assets/nested/",
		"/assets/missing.js", "/assets/../private.txt", "/assets/%2e%2e/private.txt",
		"/assets/%2e%2e%5cprivate.txt", "/assets/app-abc123.js:private.txt", "/assets//app-abc123.js",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404", path, response.Code)
			}
			if strings.Contains(response.Body.String(), "not-public") {
				t.Fatal("private build file was exposed")
			}
		})
	}
}

func TestRejectsAssetSymlinkOutsideBuild(t *testing.T) {
	dir := buildFixture(t)
	secret := filepath.Join(t.TempDir(), "secret.js")
	if err := os.WriteFile(secret, []byte("outside-secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "assets", "escape.js")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows account cannot create symlinks: %v", err)
		}
		t.Fatal(err)
	}
	router := routerWithUI(t, dir)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/escape.js", nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "outside-secret") {
		t.Fatalf("outside symlink = %d %q", response.Code, response.Body.String())
	}
}

func TestRejectsInvalidBuildWithoutRegisteringRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "index.html"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", dir} {
		router := gin.New()
		if err := Register(router, candidate); err == nil {
			t.Fatal("expected invalid build error")
		}
		if len(router.Routes()) != 0 {
			t.Fatal("invalid build registered routes")
		}
	}
}
