package platform

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	gdi32                         = windows.NewLazySystemDLL("gdi32.dll")
	procEnumDisplayMonitors       = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW           = user32.NewProc("GetMonitorInfoW")
	procSystemParametersInfo      = user32.NewProc("SystemParametersInfoW")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procShowWindow                = user32.NewProc("ShowWindow")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procPeekMessageW              = user32.NewProc("PeekMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procLoadCursorW               = user32.NewProc("LoadCursorW")
	procMsgWaitForMultipleObjects = user32.NewProc("MsgWaitForMultipleObjects")
	procGetDC                     = user32.NewProc("GetDC")
	procReleaseDC                 = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow       = user32.NewProc("UpdateLayeredWindow")
	procSetWindowRgn              = user32.NewProc("SetWindowRgn")
	procSetCapture                = user32.NewProc("SetCapture")
	procReleaseCapture            = user32.NewProc("ReleaseCapture")
	procGetModuleHandleW          = kernel32.NewProc("GetModuleHandleW")
	procCreateCompatibleDC        = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection          = gdi32.NewProc("CreateDIBSection")
	procSelectObject              = gdi32.NewProc("SelectObject")
	procDeleteObject              = gdi32.NewProc("DeleteObject")
	procDeleteDC                  = gdi32.NewProc("DeleteDC")
	procExtCreateRegion           = gdi32.NewProc("ExtCreateRegion")
	procCreateRectRgn             = gdi32.NewProc("CreateRectRgn")
)

// SetWindowPos uFlags. 外部ウィンドウの移動 (MoveExternalWindow) で使用する。
const (
	swpNoSize         uint32 = 0x0001
	swpNoZOrder       uint32 = 0x0004
	swpNoActivate     uint32 = 0x0010
	swpAsyncWindowPos uint32 = 0x4000
)

type point struct {
	X, Y int32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

// ScreenInfo は 1 モニタ分の物理範囲と WorkArea (タスクバー除く)。
type ScreenInfo struct {
	Monitor  image.Rectangle
	WorkArea image.Rectangle
}

// EnumDisplayMonitors のコールバックは package var に 1 個だけ作って使い回す。
// syscall.NewCallback で作ったコールバックは GC されないため、毎回作るとリークする。
var (
	enumMu      sync.Mutex
	enumResults []ScreenInfo
	enumProc    = syscall.NewCallback(func(hMonitor, _, _, _ uintptr) uintptr {
		var mi monitorInfo
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		ret, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
		if ret != 0 {
			enumResults = append(enumResults, ScreenInfo{
				Monitor: image.Rect(
					int(mi.RcMonitor.Left), int(mi.RcMonitor.Top),
					int(mi.RcMonitor.Right), int(mi.RcMonitor.Bottom),
				),
				WorkArea: image.Rect(
					int(mi.RcWork.Left), int(mi.RcWork.Top),
					int(mi.RcWork.Right), int(mi.RcWork.Bottom),
				),
			})
		}
		return 1 // 列挙継続
	})
)

// Screens は全モニタの物理範囲 + WorkArea を仮想デスクトップ座標で返す。
func Screens() []ScreenInfo {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumResults = enumResults[:0]
	procEnumDisplayMonitors.Call(0, 0, enumProc, 0)
	out := make([]ScreenInfo, len(enumResults))
	copy(out, enumResults)
	if len(out) == 0 {
		// EnumDisplayMonitors が動かない環境のフォールバック
		const SPI_GETWORKAREA = 0x0030
		var r rect
		ret, _, _ := procSystemParametersInfo.Call(SPI_GETWORKAREA, 0, uintptr(unsafe.Pointer(&r)), 0)
		if ret != 0 {
			wa := image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
			out = append(out, ScreenInfo{Monitor: wa, WorkArea: wa})
		}
	}
	return out
}

// CursorPosition は仮想デスクトップ座標でカーソル位置を返す。
func CursorPosition() image.Point {
	var p point
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if ret == 0 {
		return image.Point{}
	}
	return image.Point{X: int(p.X), Y: int(p.Y)}
}

// ----------- Win32 ウィンドウ プリミティブ -----------

// Win32 ウィンドウスタイル定数。
const (
	wsPopup        uint32 = 0x80000000
	wsVisible      uint32 = 0x10000000
	wsExToolWindow uint32 = 0x00000080
	wsExTopMost    uint32 = 0x00000008
	wsExNoActivate uint32 = 0x08000000
	wsExLayered    uint32 = 0x00080000

	swHide           uintptr = 0
	swShowNoActivate uintptr = 4
	swShow           uintptr = 5

	wmDestroy     uint32 = 0x0002
	wmClose       uint32 = 0x0010
	wmQuit        uint32 = 0x0012
	wmMouseMove   uint32 = 0x0200
	wmLButtonDown uint32 = 0x0201
	wmLButtonUp   uint32 = 0x0202
	wmRButtonDown uint32 = 0x0204

	pmRemove uintptr = 0x0001

	idcArrow uintptr = 32512
)

// wndClassEx は Win32 WNDCLASSEXW 構造体。
type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// msgStruct は Win32 MSG 構造体。
type msgStruct struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

// Win32Window は単一の Win32 ウィンドウハンドル。
// Layered=true で生成すると WS_EX_LAYERED 付きの透過ウィンドウとなり、
// SetBitmap で premultiplied BGRA 画像を流し込んで表示する。
type Win32Window struct {
	hwnd     uintptr
	layered  bool
	handlers WindowHandlers
}

// WindowHandlers は Win32Window へのマウス・破棄イベントを Go 側へ通知する
// コールバック群。nil を許容する。
type WindowHandlers struct {
	OnLeftDown  func(localX, localY int)
	OnLeftUp    func(localX, localY int)
	OnRightDown func(localX, localY int)
	OnMouseMove func(localX, localY int)
}

// WindowOpts は NewWin32Window のオプション。
// Layered=true なら透過 (UpdateLayeredWindow) 経路、false なら不透明。
type WindowOpts struct {
	Title         string
	X, Y          int
	Width, Height int
	Layered       bool
	Handlers      WindowHandlers
}

var (
	classOnce      sync.Once
	classRegErr    error
	classNameUTF16 = windows.StringToUTF16Ptr("BunashimejiWindow")

	// syscall.NewCallback で作るコールバックは GC されないように package var で保持する。
	sharedWndProc = syscall.NewCallback(wndProcDispatch)

	// hwnd → Win32Window* のグローバル登録簿。
	// GWLP_USERDATA に Go ポインタを格納すると GC で動く可能性があり面倒なため、
	// uintptr(hwnd) をキーに別途持つ。WndProc から登録ハンドラを引くのに使う。
	windowsMu     sync.RWMutex
	windowsByHwnd = map[uintptr]*Win32Window{}
)

// wndProcDispatch は全ウィンドウ共通の WndProc。
// グローバル登録簿から hwnd → Win32Window を引き、ハンドラに分配する。
// 該当ウィンドウが登録される前に届くメッセージ (WM_NCCREATE 等) は DefWindowProc 任せ。
func wndProcDispatch(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	windowsMu.RLock()
	w := windowsByHwnd[hwnd]
	windowsMu.RUnlock()

	switch msg {
	case wmLButtonDown:
		x, y := decodeMouseCoords(lparam)
		// ドラッグ中にカーソルがウィンドウ外へ出ても WM_LBUTTONUP を取りこぼさないよう
		// SetCapture でマウスを捕捉する。
		procSetCapture.Call(hwnd)
		if w != nil && w.handlers.OnLeftDown != nil {
			w.handlers.OnLeftDown(x, y)
		}
		return 0
	case wmLButtonUp:
		x, y := decodeMouseCoords(lparam)
		procReleaseCapture.Call()
		if w != nil && w.handlers.OnLeftUp != nil {
			w.handlers.OnLeftUp(x, y)
		}
		return 0
	case wmRButtonDown:
		x, y := decodeMouseCoords(lparam)
		if w != nil && w.handlers.OnRightDown != nil {
			w.handlers.OnRightDown(x, y)
		}
		return 0
	case wmMouseMove:
		x, y := decodeMouseCoords(lparam)
		if w != nil && w.handlers.OnMouseMove != nil {
			w.handlers.OnMouseMove(x, y)
		}
		return 0
	case wmDestroy:
		// 個別ウィンドウ破棄時は QUIT を送らない。アプリ終了は別経路で制御する。
		return defWindowProc(hwnd, msg, wparam, lparam)
	default:
		return defWindowProc(hwnd, msg, wparam, lparam)
	}
}

// decodeMouseCoords は WM_MOUSE* の lparam から (x, y) を取り出す。
// 16 bit signed なので int16 経由で sign-extend する (negative coords は capture 中に発生しうる)。
func decodeMouseCoords(lparam uintptr) (int, int) {
	x := int(int16(lparam & 0xffff))
	y := int(int16((lparam >> 16) & 0xffff))
	return x, y
}

func defWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return ret
}

// ensureClassRegistered は初回のみウィンドウクラスを登録する。
func ensureClassRegistered() error {
	classOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleW.Call(0)
		cur, _, _ := procLoadCursorW.Call(0, idcArrow)
		wc := wndClassEx{
			cbSize:      uint32(unsafe.Sizeof(wndClassEx{})),
			style:       0,
			lpfnWndProc: sharedWndProc,
			hInstance:   hInst,
			hCursor:     cur,
			// hbrBackground=6 (COLOR_WINDOW+1) で不透明 (Layered=false) パスの背景を可視化する。
			// Layered ウィンドウは UpdateLayeredWindow が描画するため、この値は使われない。
			hbrBackground: 6,
			lpszClassName: classNameUTF16,
		}
		atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			classRegErr = fmt.Errorf("RegisterClassExW: %w", callErr)
		}
	})
	return classRegErr
}

// NewWin32Window は最小構成 (枠なし・タスクバー非表示・最前面・非アクティブ化) の
// Win32 ウィンドウを生成する。
// Layered=true: WS_EX_LAYERED 付き、初期は非表示 (SetBitmap → Show で表示)
// Layered=false: 不透明、生成と同時に表示
func NewWin32Window(opts WindowOpts) (*Win32Window, error) {
	if err := ensureClassRegistered(); err != nil {
		return nil, err
	}
	titlePtr := windows.StringToUTF16Ptr(opts.Title)
	hInst, _, _ := procGetModuleHandleW.Call(0)

	style := wsPopup
	exStyle := wsExToolWindow | wsExTopMost | wsExNoActivate
	if opts.Layered {
		// Layered ウィンドウは初期は中身なし → ShowWindow を遅延させる。
		// WS_VISIBLE を付けない代わりに最初の SetBitmap 内で Show を呼ぶ。
		exStyle |= wsExLayered
	} else {
		// 不透明ウィンドウは即可視化
		style |= wsVisible
	}

	hwnd, _, callErr := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(classNameUTF16)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(style),
		uintptr(opts.X), uintptr(opts.Y),
		uintptr(opts.Width), uintptr(opts.Height),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW: %w", callErr)
	}
	w := &Win32Window{hwnd: hwnd, layered: opts.Layered, handlers: opts.Handlers}
	windowsMu.Lock()
	windowsByHwnd[hwnd] = w
	windowsMu.Unlock()
	return w, nil
}

// Destroy はウィンドウを破棄する。多重呼び出しは無害。
func (w *Win32Window) Destroy() {
	if w.hwnd != 0 {
		windowsMu.Lock()
		delete(windowsByHwnd, w.hwnd)
		windowsMu.Unlock()
		procDestroyWindow.Call(w.hwnd)
		w.hwnd = 0
	}
}

// HWND は内部のウィンドウハンドルを返す (テスト用途)。
func (w *Win32Window) HWND() uintptr { return w.hwnd }

// Show は layered ウィンドウを表示する (SW_SHOWNOACTIVATE)。
// 不透明ウィンドウは生成時に既に表示済みなので呼ぶ必要はない。
func (w *Win32Window) Show() {
	procShowWindow.Call(w.hwnd, swShowNoActivate)
}

// Hide はウィンドウを非表示にする。
func (w *Win32Window) Hide() {
	procShowWindow.Call(w.hwnd, swHide)
}

// ----------- レイヤードウィンドウ描画 (UpdateLayeredWindow) -----------

const (
	biRGB        uint32  = 0
	dibRGBColors uintptr = 0
	ulwAlpha     uintptr = 0x00000002
	acSrcOver    byte    = 0x00
	acSrcAlpha   byte    = 0x01
)

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type sizeStruct struct {
	Cx, Cy int32
}

// SetBitmap は layered ウィンドウの内容を img で更新する。
// img は Go 標準の image.RGBA (straight alpha)。内部で premultiplied BGRA に変換する。
// onscreenPos は仮想デスクトップ絶対座標でのウィンドウ左上位置。
//
// UpdateLayeredWindow はサイズ・位置・ビットマップ内容を一度に更新するため、
// pose 切替と位置移動を 1 回の呼び出しでまとめられる。
func (w *Win32Window) SetBitmap(img *image.RGBA, onscreenPos image.Point) error {
	if !w.layered {
		return errors.New("SetBitmap requires Layered=true window")
	}
	if w.hwnd == 0 {
		return errors.New("window already destroyed")
	}
	bounds := img.Bounds()
	width := int32(bounds.Dx())
	height := int32(bounds.Dy())
	if width <= 0 || height <= 0 {
		return errors.New("empty bitmap")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return errors.New("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	// biHeight を負にすることで top-down DIB (image.RGBA と同じ pixel 順) になる。
	bi := bitmapInfo{
		BmiHeader: bitmapInfoHeader{
			BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			BiWidth:       width,
			BiHeight:      -height,
			BiPlanes:      1,
			BiBitCount:    32,
			BiCompression: biRGB,
		},
	}
	var bits unsafe.Pointer
	hbm, _, _ := procCreateDIBSection.Call(
		memDC,
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if hbm == 0 {
		return errors.New("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(hbm)

	// image.RGBA (RGBA, straight alpha) → DIB (BGRA, premultiplied alpha) 変換。
	// stride を考慮して行単位でコピーする (image.RGBA は stride > 4*W のことがある)。
	src := img.Pix
	srcStride := img.Stride
	dstStride := int(width * 4)
	dst := unsafe.Slice((*byte)(bits), int(width)*int(height)*4)
	for y := 0; y < int(height); y++ {
		srcRow := src[y*srcStride : y*srcStride+int(width)*4]
		dstRow := dst[y*dstStride : y*dstStride+dstStride]
		for x := 0; x < int(width); x++ {
			r := srcRow[x*4+0]
			g := srcRow[x*4+1]
			b := srcRow[x*4+2]
			a := srcRow[x*4+3]
			// premultiply: c' = c * a / 255
			dstRow[x*4+0] = byte(uint16(b) * uint16(a) / 255)
			dstRow[x*4+1] = byte(uint16(g) * uint16(a) / 255)
			dstRow[x*4+2] = byte(uint16(r) * uint16(a) / 255)
			dstRow[x*4+3] = a
		}
	}

	oldObj, _, _ := procSelectObject.Call(memDC, hbm)
	defer procSelectObject.Call(memDC, oldObj)

	blend := blendFunction{
		BlendOp:             acSrcOver,
		BlendFlags:          0,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}
	sz := sizeStruct{Cx: width, Cy: height}
	ptDst := point{X: int32(onscreenPos.X), Y: int32(onscreenPos.Y)}
	ptSrc := point{X: 0, Y: 0}

	ret, _, callErr := procUpdateLayeredWindow.Call(
		w.hwnd,
		screenDC,
		uintptr(unsafe.Pointer(&ptDst)),
		uintptr(unsafe.Pointer(&sz)),
		memDC,
		uintptr(unsafe.Pointer(&ptSrc)),
		0, // crKey (使わない)
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
	if ret == 0 {
		return fmt.Errorf("UpdateLayeredWindow: %w", callErr)
	}
	return nil
}

// ----------- クリック領域マスク (SetWindowRgn) -----------

const (
	rdhRectangles uint32 = 1
	// 不透明判定の alpha しきい値。1 以上で「クリック可能」として登録する。
	// 本家互換のため、ごく薄いアンチエイリアス縁もクリック対象に含めたい場合は低めに。
	defaultAlphaThreshold uint8 = 1
)

type rgnDataHeader struct {
	DwSize   uint32
	IType    uint32
	NCount   uint32
	NRgnSize uint32
	RcBound  rect
}

// opaqueRectsFromRGBA は RGBA 画像の alpha>=threshold ピクセルを行ごとに run-length 圧縮し、
// クリック領域用の矩形配列を返す。空配列の場合はクリック領域なし (= 全クリックスルー)。
func opaqueRectsFromRGBA(img *image.RGBA, threshold uint8) []rect {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	var out []rect
	for y := 0; y < h; y++ {
		base := y * img.Stride
		runStart := -1
		for x := 0; x < w; x++ {
			a := img.Pix[base+x*4+3]
			if a >= threshold {
				if runStart == -1 {
					runStart = x
				}
			} else if runStart != -1 {
				out = append(out, rect{
					Left: int32(runStart), Top: int32(y),
					Right: int32(x), Bottom: int32(y + 1),
				})
				runStart = -1
			}
		}
		if runStart != -1 {
			out = append(out, rect{
				Left: int32(runStart), Top: int32(y),
				Right: int32(w), Bottom: int32(y + 1),
			})
		}
	}
	return out
}

// boundingBox は矩形配列の最小外接矩形を返す (空ならゼロ矩形)。
func boundingBox(rects []rect) rect {
	if len(rects) == 0 {
		return rect{}
	}
	bb := rects[0]
	for _, r := range rects[1:] {
		if r.Left < bb.Left {
			bb.Left = r.Left
		}
		if r.Top < bb.Top {
			bb.Top = r.Top
		}
		if r.Right > bb.Right {
			bb.Right = r.Right
		}
		if r.Bottom > bb.Bottom {
			bb.Bottom = r.Bottom
		}
	}
	return bb
}

// createRegionFromRects は矩形配列から HRGN を 1 回の ExtCreateRegion で作る。
// 矩形ごとに CreateRectRgn + CombineRgn するより N 倍以上速い。
// 戻り値の HRGN は呼び出し側が DeleteObject か SetWindowRgn の所有権譲渡で解放する。
func createRegionFromRects(rects []rect) (uintptr, error) {
	if len(rects) == 0 {
		return 0, nil
	}
	headerSize := int(unsafe.Sizeof(rgnDataHeader{}))
	rectSize := int(unsafe.Sizeof(rect{}))
	buf := make([]byte, headerSize+len(rects)*rectSize)
	hdr := (*rgnDataHeader)(unsafe.Pointer(&buf[0]))
	hdr.DwSize = uint32(headerSize)
	hdr.IType = rdhRectangles
	hdr.NCount = uint32(len(rects))
	hdr.NRgnSize = uint32(len(rects) * rectSize)
	hdr.RcBound = boundingBox(rects)

	dst := unsafe.Slice((*rect)(unsafe.Pointer(&buf[headerSize])), len(rects))
	copy(dst, rects)

	hrgn, _, callErr := procExtCreateRegion.Call(
		0, // XFORM = identity
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if hrgn == 0 {
		return 0, fmt.Errorf("ExtCreateRegion: %w", callErr)
	}
	return hrgn, nil
}

// SetClickMask はクリック可能領域を img の alpha チャネルから設定する。
// SetWindowRgn は HRGN の所有権を OS に譲渡するため、呼び出し後に DeleteObject してはいけない。
// 領域更新は SetBitmap と同じタイミングで行えば pose 切替時に自動追従する。
func (w *Win32Window) SetClickMask(img *image.RGBA) error {
	if w.hwnd == 0 {
		return errors.New("window already destroyed")
	}
	rects := opaqueRectsFromRGBA(img, defaultAlphaThreshold)
	hrgn, err := createRegionFromRects(rects)
	if err != nil {
		return err
	}
	// hrgn=0 (NULL) を SetWindowRgn に渡すと「region 解除 = 全領域クリック可」になる。
	// マスコットで意図する挙動 (全 alpha=0 → クリックスルー) と逆なので、
	// 矩形ゼロのときは CreateRectRgn(0,0,0,0) で空 region を作って渡す。
	if hrgn == 0 {
		hrgn, _, _ = procCreateRectRgn.Call(0, 0, 0, 0)
	}
	ret, _, callErr := procSetWindowRgn.Call(w.hwnd, hrgn, 1)
	if ret == 0 {
		// SetWindowRgn が失敗した場合は HRGN の所有権が呼び出し側に残るので解放する。
		procDeleteObject.Call(hrgn)
		return fmt.Errorf("SetWindowRgn: %w", callErr)
	}
	return nil
}

// ----------- メッセージループ + tick スケジューラ -----------

const (
	qsAllInput uintptr = 0x04FF
	// MsgWaitForMultipleObjects の戻り値: タイムアウト
	waitTimeout uintptr = 0x00000102
)

// RunMessageLoop はカレントスレッド上で Win32 メッセージポンプを回し、
// tickInterval ごとに onTick を呼ぶ。runtime.LockOSThread 済みであること。
//
// 終了条件は以下のいずれか:
//   - ctx がキャンセルされる
//   - WM_QUIT が届く (PostQuitMessage)
//   - onTick から panic
//
// メッセージ待ちは MsgWaitForMultipleObjects を使用し、入力がない間は CPU を消費しない。
// time.Sleep ベースのループより入力応答が良い。
func RunMessageLoop(ctx context.Context, tickInterval time.Duration, onTick func()) error {
	deadline := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if PumpMessages() {
			return nil
		}
		now := time.Now()
		if !now.Before(deadline) {
			onTick()
			deadline = deadline.Add(tickInterval)
			// 大きく遅れた場合は catch-up せずに現在時刻基準でリセットする
			if time.Now().After(deadline.Add(tickInterval)) {
				deadline = time.Now().Add(tickInterval)
			}
		}
		// 次の deadline まで「メッセージ到着 or タイムアウト」のどちらか早い方で起きる。
		// ctx キャンセルへの応答を最大 tickInterval 内に保つため、
		// 1 回の wait は tickInterval を超えないようにする。
		waitMs := time.Until(deadline).Milliseconds()
		if waitMs <= 0 {
			waitMs = 1
		}
		procMsgWaitForMultipleObjects.Call(
			0, 0, 0,
			uintptr(uint32(waitMs)),
			qsAllInput,
		)
	}
}

// PumpMessages は現在キューに溜まっているメッセージを全消化する (ノンブロッキング)。
// WM_QUIT を受けたら true を返す。
func PumpMessages() bool {
	var m msgStruct
	for {
		ret, _, _ := procPeekMessageW.Call(
			uintptr(unsafe.Pointer(&m)),
			0, 0, 0, pmRemove,
		)
		if ret == 0 {
			return false
		}
		if m.Message == wmQuit {
			return true
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
