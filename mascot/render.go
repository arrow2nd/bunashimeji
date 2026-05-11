package mascot

import (
	"image"
	"image/draw"
)

// rgbaCacheKey は CharacterTemplate 内の RGBA キャッシュキー。
// 元画像ポインタと向きの組で一意。
type rgbaCacheKey struct {
	img       image.Image
	lookRight bool
}

// RGBA は src を *image.RGBA (straight alpha) として返す。
// lookRight=true なら水平反転版を返す (元画像は左向きで描かれている前提)。
//
// 結果はテンプレート内で個体間共有される。同キャラ N 体が同じ pose を
// 表示しても RGBA バッファは 1 セットしか持たないため、メモリ使用量が
// 個体数に対してリニアに伸びない。
//
// 呼び出しは Tick callback (= 単一スレッド) からのみを想定し、ロックは取らない。
func (t *CharacterTemplate) RGBA(src image.Image, lookRight bool) *image.RGBA {
	key := rgbaCacheKey{img: src, lookRight: lookRight}
	if rgba, ok := t.rgbaCache[key]; ok {
		return rgba
	}
	rgba := toRGBA(src, lookRight)
	t.rgbaCache[key] = rgba
	return rgba
}

// toRGBA は image.Image を *image.RGBA (straight alpha) に変換する。
// lookRight=true なら水平反転して返す (元画像は左向きで描かれている前提)。
func toRGBA(src image.Image, lookRight bool) *image.RGBA {
	bounds := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(out, out.Bounds(), src, bounds.Min, draw.Src)
	if lookRight {
		flipHorizontal(out)
	}
	return out
}

// flipHorizontal は RGBA を in-place で水平反転する。
func flipHorizontal(rgba *image.RGBA) {
	w := rgba.Bounds().Dx()
	h := rgba.Bounds().Dy()
	for y := 0; y < h; y++ {
		row := rgba.Pix[y*rgba.Stride : y*rgba.Stride+w*4]
		for x := 0; x < w/2; x++ {
			i := x * 4
			j := (w - 1 - x) * 4
			row[i+0], row[j+0] = row[j+0], row[i+0]
			row[i+1], row[j+1] = row[j+1], row[i+1]
			row[i+2], row[j+2] = row[j+2], row[i+2]
			row[i+3], row[j+3] = row[j+3], row[i+3]
		}
	}
}
