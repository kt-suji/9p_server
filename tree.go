package main

import (
	"strings"
	"sync"

	"github.com/knusbaum/go9p/fs"
	"github.com/knusbaum/go9p/proto"
)

type TreeBuilder struct {
	p9FS *fs.FS
	root *fs.StaticDir
	mu   sync.Mutex
}

func NewTreeBuilder(p9FS *fs.FS, root *fs.StaticDir) *TreeBuilder {
	return &TreeBuilder{p9FS: p9FS, root: root}
}

func findChildDir(dir *fs.StaticDir, name string) *fs.StaticDir {
	for _, child := range dir.Children() {
		if child.Stat().Name == name {
			if d, ok := child.(*fs.StaticDir); ok {
				return d
			}
		}
	}
	return nil
}

// 中間ディレクトリがなければ作成
func (tb *TreeBuilder) ensureDir(dirPath string) (*fs.StaticDir, error) {
	if dirPath == "" || dirPath == "." {
		return tb.root, nil
	}

	parts := strings.Split(strings.Trim(dirPath, "/"), "/")
	curr := tb.root

	for _, part := range parts {
		found := findChildDir(curr, part)
		if found == nil {
			stat := tb.p9FS.NewStat(part, "user", "user", 0755|proto.DMDIR)
			newDir := fs.NewStaticDir(stat)
			curr.AddChild(newDir)
			curr = newDir
		} else {
			curr = found
		}
	}
	return curr, nil
}

func (tb *TreeBuilder) RegisterRead(fullPath string, rFn DynamicReader) error {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	dirPath, fileName := splitPath(fullPath)
	parentDir, err := tb.ensureDir(dirPath)
	if err != nil {
		return err
	}
	for _, child := range parentDir.Children() {
		if child.Stat().Name == fileName {
			if df, ok := child.(*dynamicFile); ok {
				df.readFunc = rFn
				return nil
			}
			return nil
		}
	}
	stat := tb.p9FS.NewStat(fileName, "user", "user", 0444)
	return parentDir.AddChild(newDynamicRWFile(stat, rFn, nil))
}

func (tb *TreeBuilder) RegisterRW(fullPath string, rFn DynamicReader, wFn DynamicWriter) error {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	dirPath, fileName := splitPath(fullPath)
	parentDir, err := tb.ensureDir(dirPath)
	if err != nil {
		return err
	}
	for _, child := range parentDir.Children() {
		if child.Stat().Name == fileName {
			if df, ok := child.(*dynamicFile); ok {
				df.readFunc = rFn
				df.writeFunc = wFn
				return nil
			}
			return nil
		}
	}
	stat := tb.p9FS.NewStat(fileName, "user", "user", 0666)
	return parentDir.AddChild(newDynamicRWFile(stat, rFn, wFn))
}

func splitPath(path string) (dir, file string) {
	idx := strings.LastIndex(path, "/")
	if idx == -1 {
		return "", path
	}
	return path[:idx], path[idx+1:]
}
