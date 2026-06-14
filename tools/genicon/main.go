// genicon は buna.png を読んで buna.ico (PNG-in-ICO, Vista+) を吐き出すワンショット
// ユーティリティ。後段で `go run github.com/akavel/rsrc@latest -ico buna.ico -o rsrc_windows.syso`
// を回すと exe アイコン用の .syso が出来上がる。
//
// 使い方: リポジトリルートで `go run ./tools/genicon`
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"log"
	"os"
)

const (
	srcPath = "buna.png"
	dstPath = "buna.ico"
)

func main() {
	pngBytes, err := os.ReadFile(srcPath)
	if err != nil {
		log.Fatalf("read %s: %v", srcPath, err)
	}
	ico, err := pngToICO(pngBytes)
	if err != nil {
		log.Fatalf("pngToICO: %v", err)
	}
	if err := os.WriteFile(dstPath, ico, 0o644); err != nil {
		log.Fatalf("write %s: %v", dstPath, err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", dstPath, len(ico))
	fmt.Println("next: go run github.com/akavel/rsrc@latest -ico buna.ico -o rsrc_windows.syso")
}

// pngToICO は tray.go と同じロジック。PNG を 1 エントリの ICO に包む。
// Windows Vista 以降は ICO エントリに PNG を直接埋められる (BMP DIB に変換不要)。
func pngToICO(pngBytes []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("decode png: %w", err)
	}
	w := byte(cfg.Width)
	if cfg.Width >= 256 {
		w = 0
	}
	h := byte(cfg.Height)
	if cfg.Height >= 256 {
		h = 0
	}
	const (
		iconDirSize   = 6
		iconEntrySize = 16
		dataOffset    = iconDirSize + iconEntrySize
	)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	buf.WriteByte(w)
	buf.WriteByte(h)
	buf.WriteByte(0)
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pngBytes)))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataOffset))
	buf.Write(pngBytes)
	return buf.Bytes(), nil
}
