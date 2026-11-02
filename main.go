package main

import (
	"log"

	"github.com/knusbaum/go9p"
	"github.com/knusbaum/go9p/fs"
)

func main() {
	cfg := loadServerConfig("config.yaml")

	p9FS, rootDir := fs.NewFS("user", "user", 0755)

	tree := NewTreeBuilder(p9FS, rootDir)
	SetupAllNodes(tree, cfg)

	log.Printf("[INFO] 9P Server listening on %s ...\n", cfg.Server.ListenAddr)
	if err := go9p.Serve(cfg.Server.ListenAddr, p9FS.Server()); err != nil {
		log.Fatalf("[ERROR] Server error: %v", err)
	}
}
