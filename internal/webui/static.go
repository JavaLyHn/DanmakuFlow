// Package webui serves the optional, pre-built browser client without adding a
// frontend runtime dependency to the Go server.
package webui

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// Register adds only the single page and its build assets. A missing or invalid
// build returns an error before adding routes, so the terminal client can keep
// using /ws even when the frontend has not been built.
func Register(router *gin.Engine, directory string) error {
	if directory == "" {
		return fmt.Errorf("frontend build directory is empty")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve frontend build directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open frontend build: %w", err)
	}
	defer root.Close()
	index, err := root.Open("index.html")
	if err != nil {
		return fmt.Errorf("open frontend index: %w", err)
	}
	defer index.Close()
	info, err := index.Stat()
	if err != nil {
		return fmt.Errorf("inspect frontend index: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("frontend index.html must be a regular file")
	}

	page := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		serveFile(c, directory, "index.html")
	}
	assets := func(c *gin.Context) {
		name := strings.TrimPrefix(c.Param("filepath"), "/")
		// Reject traversal and Windows path/alternate-stream syntax before opening.
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		serveFile(c, directory, "assets/"+name)
	}
	router.GET("/", page)
	router.HEAD("/", page)
	router.GET("/assets/*filepath", assets)
	router.HEAD("/assets/*filepath", assets)
	return nil
}

func serveFile(c *gin.Context, directory, name string) {
	// OpenRoot confines symlink resolution to the build directory too. Opening
	// it for each request keeps resource ownership local to the handler.
	root, err := os.OpenRoot(directory)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}
