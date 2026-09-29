package materialbalance

import (
	"errors"
	"math"
)

// 读数校验错误。
var (
	ErrUnknownPoint       = errors.New("unknown meter point")
	ErrNegativeValue      = errors.New("meter value must be >= 0")
	ErrBadTimestamp       = errors.New("reading timestamp must be positive")
	ErrMeterWentBackwards = errors.New("cumulative meter regressed")
	ErrDuplicateTimestamp = errors.New("duplicate timestamp for meter point")
)

// 窗口状态错误（在 book.go 也会用到，集中声明）。
var (
	ErrWindowAlreadyOpen = errors.New("a balance window is already open")
	ErrSourceRequired    = errors.New("feed source is required")
	ErrBeforeFirstWindow = errors.New("reading timestamp is before the first balance window")
	ErrAfterLastWindow   = errors.New("reading timestamp is after the last balance window")
)

// ParsePoint 把字符串解析为计量点。
func ParsePoint(s string) (Point, error) {
	p := Point(s)
	if !p.Valid() {
		// 保持哨兵错误身份，调用方可用 errors.Is 判断。
		return "", ErrUnknownPoint
	}
	return p, nil
}

// Valid 判断计量点是否受支持。
func (p Point) Valid() bool {
	switch p {
	case PointFeed, PointOverhead, PointBottoms, PointInventory:
		return true
	default:
		return false
	}
}

// Cumulative 判断是否为累积式流量计；设备存量是瞬时计。
func (p Point) Cumulative() bool {
	switch p {
	case PointFeed, PointOverhead, PointBottoms:
		return true
	default:
		return false
	}
}

// validateReading 做单条读数自身的合法性校验（不含窗口内单调性/去重）。
func validateReading(r Reading) error {
	if !r.Point.Valid() {
		return ErrUnknownPoint
	}
	if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 {
		return ErrNegativeValue
	}
	if r.At <= 0 {
		return ErrBadTimestamp
	}
	return nil
}

// checkSequence 在校验同一计量点、已按时间戳排序的读数基础上，
// 检查累积表单调不减并对同一时间戳去重。inventory 仅去重。
func checkSequence(point Point, ordered []Reading, incoming Reading) error {
	for _, r := range ordered {
		if r.At == incoming.At {
			return ErrDuplicateTimestamp
		}
	}
	if !point.Cumulative() {
		return nil
	}
	// ordered 已按 At 升序；找插入位置并比较时间序邻居（与到达顺序无关）。
	prev := -1
	next := -1
	for i, r := range ordered {
		if r.At < incoming.At {
			prev = i
		}
		if r.At > incoming.At && next == -1 {
			next = i
		}
	}
	if prev >= 0 && incoming.Value < ordered[prev].Value {
		return ErrMeterWentBackwards
	}
	if next >= 0 && incoming.Value > ordered[next].Value {
		return ErrMeterWentBackwards
	}
	return nil
}
