package main

import (
	"strings"

	"github.com/knusbaum/go9p/fs"
)

type TreeBuilder struct {
	p9FS *fs.FS
	root *fs.StaticDir
}

func NewTreeBuilder(p9FS *fs.FS, root *fs.StaticDir) *TreeBuilder {
	return &TreeBuilder{p9FS: p9FS, root: root}
}

// findChildDir は StaticDir の配下に同名の子ディレクトリが存在するか探す
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

// ensureDir はパスの中間ディレクトリが存在しなければ自動生成して辿る
func (tb *TreeBuilder) ensureDir(dirPath string) (*fs.StaticDir, error) {
	if dirPath == "" || dirPath == "." {
		return tb.root, nil
	}

	parts := strings.Split(strings.Trim(dirPath, "/"), "/")
	curr := tb.root

	for _, part := range parts {
		found := findChildDir(curr, part)
		if found == nil {
			stat := tb.p9FS.NewStat(part, "user", "user", 0755)
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
	dirPath, fileName := splitPath(fullPath)
	parentDir, err := tb.ensureDir(dirPath)
	if err != nil {
		return err
	}
	stat := tb.p9FS.NewStat(fileName, "user", "user", 0444)
	parentDir.AddChild(newDynamicRWFile(stat, rFn, nil))
	return nil
}

func (tb *TreeBuilder) RegisterRW(fullPath string, rFn DynamicReader, wFn DynamicWriter) error {
	dirPath, fileName := splitPath(fullPath)
	parentDir, err := tb.ensureDir(dirPath)
	if err != nil {
		return err
	}
	stat := tb.p9FS.NewStat(fileName, "user", "user", 0666)
	parentDir.AddChild(newDynamicRWFile(stat, rFn, wFn))
	return nil
}

func splitPath(path string) (dir, file string) {
	idx := strings.LastIndex(path, "/")
	if idx == -1 {
		return "", path
	}
	return path[:idx], path[idx+1:]
}
