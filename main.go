package main

import (
	"log"

	"github.com/knusbaum/go9p"
	"github.com/knusbaum/go9p/fs"
)

func main() {
	// fs.NewFS の戻り値 rootDir は最初から *fs.StaticDir
	p9FS, rootDir := fs.NewFS("user", "user", 0755)

	// TreeBuilder にそのまま渡す
	tree := NewTreeBuilder(p9FS, rootDir)

	// nodes.go の全ノードを一括展開
	SetupAllNodes(tree)

	log.Println("9P Server listening on :5640 ...")
	// Srv インターフェースを実装しているのは p9FS (または p9FS.Server())
	if err := go9p.Serve(":5640", p9FS.Server()); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
