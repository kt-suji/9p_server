package main

import (
	"fmt"
	"sync"

	"github.com/knusbaum/go9p/fs"
	"github.com/knusbaum/go9p/proto"
)

type DynamicReader func() ([]byte, error)
type DynamicWriter func(data []byte) error

type dynamicFile struct {
	*fs.BaseFile
	readFunc  DynamicReader
	writeFunc DynamicWriter

	// fidごとのバッファキャッシュ
	mu            sync.Mutex
	readSessions  map[uint64][]byte
	writeSessions map[uint64][]byte
}

func newDynamicFile(stat *proto.Stat, rFn DynamicReader) *dynamicFile {
	return newDynamicRWFile(stat, rFn, nil)
}

func newDynamicRWFile(stat *proto.Stat, rFn DynamicReader, wFn DynamicWriter) *dynamicFile {
	return &dynamicFile{
		BaseFile:      fs.NewBaseFile(stat),
		readFunc:      rFn,
		writeFunc:     wFn,
		readSessions:  make(map[uint64][]byte),
		writeSessions: make(map[uint64][]byte),
	}
}

func (d *dynamicFile) Open(fid uint64, omode proto.Mode) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.writeSessions, fid)
	return nil
}

func (d *dynamicFile) Close(fid uint64) error {
	// clunk時にキャッシュ破棄 & 蓄積された書き込みバッファを確定実行
	d.mu.Lock()
	delete(d.readSessions, fid)
	wbuf, hasWrite := d.writeSessions[fid]
	if hasWrite {
		delete(d.writeSessions, fid)
	}
	d.mu.Unlock()

	if hasWrite && d.writeFunc != nil && len(wbuf) > 0 {
		return d.writeFunc(wbuf)
	}
	return nil
}

func (d *dynamicFile) Read(fid uint64, offset uint64, count uint64) ([]byte, error) {
	if d.readFunc == nil {
		return nil, fmt.Errorf("read not permitted")
	}

	d.mu.Lock()
	data, exists := d.readSessions[fid]
	// 初回read時にデータ生成
	if offset == 0 || !exists {
		var err error
		data, err = d.readFunc()
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.readSessions[fid] = data
	}
	d.mu.Unlock()

	dataLen := uint64(len(data))
	if offset >= dataLen {
		return []byte{}, nil // EOF
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

	d.mu.Lock()
	defer d.mu.Unlock()

	buf := d.writeSessions[fid]
	reqLen := int(offset) + len(data)
	if reqLen > len(buf) {
		newBuf := make([]byte, reqLen)
		copy(newBuf, buf)
		buf = newBuf
	}
	copy(buf[offset:], data)
	d.writeSessions[fid] = buf

	return uint32(len(data)), nil
}

func (d *dynamicFile) Stat() proto.Stat {
	st := d.BaseFile.Stat()
	if d.readFunc != nil {
		if data, err := d.readFunc(); err == nil {
			st.Length = uint64(len(data))
		}
	}
	return st
}
