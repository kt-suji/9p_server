package main

import (
	"fmt"
	"sync"

	"github.com/knusbaum/go9p/fs"
	"github.com/knusbaum/go9p/proto"
)

// コールバック型の定義
type DynamicReader func() ([]byte, error)
type DynamicWriter func(data []byte) error

// dynamicFile は Read / Write を柔軟にフックできる汎用ノード
type dynamicFile struct {
	*fs.BaseFile
	readFunc  DynamicReader
	writeFunc DynamicWriter

	// FIDごとの読み出しセッションバッファ（無駄な再生成を防止）
	mu       sync.Mutex
	sessions map[uint64][]byte
}

// 読み込み専用ノードを生成するヘルパー
func newDynamicFile(stat *proto.Stat, rFn DynamicReader) *dynamicFile {
	return newDynamicRWFile(stat, rFn, nil)
}

// 読み書き両用・書き込み専用ノードを生成する汎用コンストラクタ
func newDynamicRWFile(stat *proto.Stat, rFn DynamicReader, wFn DynamicWriter) *dynamicFile {
	return &dynamicFile{
		BaseFile:  fs.NewBaseFile(stat),
		readFunc:  rFn,
		writeFunc: wFn,
		sessions:  make(map[uint64][]byte),
	}
}

func (d *dynamicFile) Open(fid uint64, omode proto.Mode) error {
	return nil
}

func (d *dynamicFile) Close(fid uint64) error {
	// クライアントがファイルを閉じた（clunk）時にバッファを破棄してメモリ解放
	d.mu.Lock()
	delete(d.sessions, fid)
	d.mu.Unlock()
	return nil
}

func (d *dynamicFile) Read(fid uint64, offset uint64, count uint64) ([]byte, error) {
	if d.readFunc == nil {
		return nil, fmt.Errorf("read not permitted")
	}

	d.mu.Lock()
	data, exists := d.sessions[fid]
	// offset == 0 かつ 未キャッシュの場合にのみ 1 回だけデータ生成を行う
	if offset == 0 || !exists {
		var err error
		data, err = d.readFunc()
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.sessions[fid] = data
	}
	d.mu.Unlock()

	dataLen := uint64(len(data))
	// 既に終端に達している場合は EOF（空スライス）を即時返却
	if offset >= dataLen {
		return []byte{}, nil
	}

	end := offset + count
	if end > dataLen {
		end = dataLen
	}

	return data[offset:end], nil
}

func (d *dynamicFile) Write(fid uint64, offset uint64, data []byte) (uint32, error) {
	if d.writeFunc == nil {
		return 0, fmt.Errorf("permission denied")
	}

	if err := d.writeFunc(data); err != nil {
		return 0, err
	}
	return uint32(len(data)), nil
}
