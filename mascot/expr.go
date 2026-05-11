package mascot

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// Evaluator は ${} / #{} を含む文字列を評価する。
//
// XML 読込時にリテラル断片と JS 式を分離し、式は goja.Compile で AST 化する。
// Once==true の式 (${}) は ActionState.CachedParams にキャッシュされ、
// それ以外 (#{}) は毎フレーム評価される。
type Evaluator struct {
	parts []exprPart
	raw   string
}

type exprPart struct {
	literal string
	prog    *goja.Program
	once    bool
	src     string
}

var exprPattern = regexp.MustCompile(`([#$])\{([^}]*)\}`)

// NewEvaluator は文字列をパースして Evaluator を作る。
// 式が含まれない場合でも Evaluator を返す (リテラル単一断片)。
func NewEvaluator(s string) (*Evaluator, error) {
	if s == "" {
		return nil, nil
	}
	ev := &Evaluator{raw: s}
	idx := exprPattern.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		ev.parts = []exprPart{{literal: s}}
		return ev, nil
	}
	cursor := 0
	for _, m := range idx {
		start, end := m[0], m[1]
		kind := s[m[2]:m[3]]
		expr := s[m[4]:m[5]]
		if start > cursor {
			ev.parts = append(ev.parts, exprPart{literal: s[cursor:start]})
		}
		prog, err := goja.Compile("", expr, false)
		if err != nil {
			return nil, fmt.Errorf("compile %q: %w", expr, err)
		}
		ev.parts = append(ev.parts, exprPart{prog: prog, once: kind == "$", src: expr})
		cursor = end
	}
	if cursor < len(s) {
		ev.parts = append(ev.parts, exprPart{literal: s[cursor:]})
	}
	return ev, nil
}

// Raw は元の文字列を返す。
func (ev *Evaluator) Raw() string {
	if ev == nil {
		return ""
	}
	return ev.raw
}

// HasOnce は ${} を含むか返す (Action 開始時にキャッシュすべきか判定)。
func (ev *Evaluator) HasOnce() bool {
	if ev == nil {
		return false
	}
	for _, p := range ev.parts {
		if p.prog != nil && p.once {
			return true
		}
	}
	return false
}

// HasFrame は #{} を含むか返す (毎フレーム評価が必要か)。
func (ev *Evaluator) HasFrame() bool {
	if ev == nil {
		return false
	}
	for _, p := range ev.parts {
		if p.prog != nil && !p.once {
			return true
		}
	}
	return false
}

// EvalString は補間結果を文字列として返す。
// cached は ${} 評価結果のキャッシュ (nil 可)。
func (ev *Evaluator) EvalString(vm *goja.Runtime, cached map[string]any) (string, error) {
	if ev == nil {
		return "", nil
	}
	var b strings.Builder
	for i, p := range ev.parts {
		if p.prog == nil {
			b.WriteString(p.literal)
			continue
		}
		v, err := ev.evalPart(vm, cached, i, p)
		if err != nil {
			return "", err
		}
		b.WriteString(toString(v))
	}
	return b.String(), nil
}

// EvalValue は単一の式を評価して goja.Value を返す。
// 文字列補間ではなく数値/真偽値が欲しい場合に使う。
// 複数断片がある場合は EvalString してから型変換する想定。
func (ev *Evaluator) EvalValue(vm *goja.Runtime, cached map[string]any) (any, error) {
	if ev == nil {
		return nil, nil
	}
	if len(ev.parts) == 1 && ev.parts[0].prog != nil {
		return ev.evalPart(vm, cached, 0, ev.parts[0])
	}
	s, err := ev.EvalString(vm, cached)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// EvalBool は真偽値として評価する。空・nil は true 扱い (Condition 未指定 = 常に真)。
func (ev *Evaluator) EvalBool(vm *goja.Runtime, cached map[string]any) (bool, error) {
	if ev == nil {
		return true, nil
	}
	v, err := ev.EvalValue(vm, cached)
	if err != nil {
		return false, err
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if x == "" || x == "false" {
			return false, nil
		}
		return true, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	case nil:
		return true, nil
	default:
		return true, nil
	}
}

// EvalInt は整数として評価する。
func (ev *Evaluator) EvalInt(vm *goja.Runtime, cached map[string]any) (int, error) {
	if ev == nil {
		return 0, nil
	}
	v, err := ev.EvalValue(vm, cached)
	if err != nil {
		return 0, err
	}
	return toInt(v), nil
}

// EvalFloat は浮動小数として評価する。
func (ev *Evaluator) EvalFloat(vm *goja.Runtime, cached map[string]any) (float64, error) {
	if ev == nil {
		return 0, nil
	}
	v, err := ev.EvalValue(vm, cached)
	if err != nil {
		return 0, err
	}
	return toFloat(v), nil
}

func (ev *Evaluator) evalPart(vm *goja.Runtime, cached map[string]any, i int, p exprPart) (any, error) {
	if p.once && cached != nil {
		key := cacheKey(i, p.src)
		if v, ok := cached[key]; ok {
			return v, nil
		}
		v, err := vm.RunProgram(p.prog)
		if err != nil {
			return nil, fmt.Errorf("eval %q: %w", p.src, err)
		}
		exported := exportValue(v)
		cached[key] = exported
		return exported, nil
	}
	v, err := vm.RunProgram(p.prog)
	if err != nil {
		return nil, fmt.Errorf("eval %q: %w", p.src, err)
	}
	return exportValue(v), nil
}

func cacheKey(i int, src string) string {
	return strconv.Itoa(i) + ":" + src
}

func exportValue(v goja.Value) any {
	if v == nil {
		return nil
	}
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		// NaN/Inf を int に直接 cast すると Go では math.MinInt64 等の極端値になり
		// (例: TargetX が NaN → -9223372036854775808)、後段の境界比較が壊れる。
		// 旧日本語版 XML の typo (例: Math.random*100) で NaN が頻発するので 0 に丸める。
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		return int(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return int(n)
	default:
		return 0
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return n
	default:
		return 0
	}
}
