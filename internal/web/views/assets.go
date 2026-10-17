package views

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Assets struct {
	Dev bool
	JS  string
	CSS []string
}

type manifestEntry struct {
	File    string   `json:"file"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
	IsEntry bool     `json:"isEntry"`
}

func LoadAssets() (Assets, error) {
	if os.Getenv("APP_ENV") != "production" {
		return Assets{Dev: true}, nil
	}

	manifestPath := filepath.Join("web", "dist", ".vite", "manifest.json")
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return Assets{}, fmt.Errorf("open Vite manifest %q: %w", manifestPath, err)
	}
	defer manifestFile.Close()

	manifest := map[string]manifestEntry{}
	if err := json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		return Assets{}, fmt.Errorf("decode Vite manifest %q: %w", manifestPath, err)
	}

	const entryKey = "web/src/main.ts"
	entry, ok := manifest[entryKey]
	if !ok || entry.File == "" {
		return Assets{}, fmt.Errorf("Vite manifest %q has no %q entry", manifestPath, entryKey)
	}

	cssFiles := make([]string, 0)
	seenCSS := make(map[string]struct{})
	seenChunks := make(map[string]struct{})
	var collectCSS func(string)
	collectCSS = func(key string) {
		if _, seen := seenChunks[key]; seen {
			return
		}
		seenChunks[key] = struct{}{}
		chunk, exists := manifest[key]
		if !exists {
			return
		}
		for _, imported := range chunk.Imports {
			collectCSS(imported)
		}
		for _, css := range chunk.CSS {
			if _, seen := seenCSS[css]; seen {
				continue
			}
			seenCSS[css] = struct{}{}
			cssFiles = append(cssFiles, "/"+filepath.ToSlash(css))
		}
	}
	collectCSS(entryKey)

	return Assets{JS: "/" + filepath.ToSlash(entry.File), CSS: cssFiles}, nil
}
