package material

import (
	"sort"

	"github.com/google/uuid"
)

// tolerance 是衡算闭合容差，避免浮点噪声造成假损失。
const tolerance = 1e-6

// window 是一个连续衡算时段。进料源切换或停机会切开新窗口；
// 边界时刻的读数以 At 归属，同时服务于前一窗口的期末与
// 后一窗口的期初，保证两个窗口都能闭合。
type window struct {
	id           string
	reason       string
	closedReason string
	revision     uuid.UUID
	feedSource   string
	feedRevision uuid.UUID
	startAt      int64
	endAt        int64
	closed       bool
	readings     []Reading

	// 最近一次重算得到的当期数值。
	received        float64
	overhead        float64
	bottoms         float64
	holdupFirst     float64
	holdupLast      float64
	netAccumulation float64
	lossLive        float64
	missing         []Point

	// 结账时冻结的原结论；迟到读数修订后仍然保留。
	verdict    string
	loss       float64
	hasVerdict bool
	amendments []Amendment
}

// Record 追加一条读数。读数先按 At 归位（支持迟到读数），
// 再按 seq 稳定排序。
func (w *window) Record(r Reading) {
	w.readings = append(w.readings, r)
	sort.SliceStable(w.readings, func(i, j int) bool {
		if w.readings[i].At != w.readings[j].At {
			return w.readings[i].At < w.readings[j].At
		}
		return w.readings[i].Seq < w.readings[j].Seq
	})
}

// readingsOf 返回某计量点在窗口内按时序排列的读数。
func (w *window) readingsOf(point Point) []Reading {
	out := make([]Reading, 0)
	for _, r := range w.readings {
		if r.Point == point {
			out = append(out, r)
		}
	}
	return out
}

func (w *window) close(at int64) {
	w.closed = true
	w.endAt = at
	w.recompute()
	// 冻结结账时的原结论，此后修订也不覆盖。
	if !w.hasVerdict {
		w.hasVerdict = true
		w.verdict = w.currentVerdict()
		w.loss = w.currentLoss()
	}
}

// recompute 依据当前全部读数（含迟到/更正后的读数）重算时段衡算。
// feed-in/overhead/bottoms 是累计总量表，时段量取末读数-首读数；
// holdup 是设备存量，首读数作期初、末读数作期末，差值即净积累。
func (w *window) recompute() {
	feed := w.readingsOf(FeedIn)
	top := w.readingsOf(Overhead)
	bottom := w.readingsOf(Bottoms)
	hold := w.readingsOf(Holdup)

	w.missing = nil
	if len(feed) == 0 {
		w.missing = append(w.missing, FeedIn)
	}
	if len(top) == 0 {
		w.missing = append(w.missing, Overhead)
	}
	if len(bottom) == 0 {
		w.missing = append(w.missing, Bottoms)
	}
	if len(hold) == 0 {
		w.missing = append(w.missing, Holdup)
	}

	// 每个点至少需要首末两个读数才能算出时段量；单点时视为尚未闭合。
	w.received = span(feed)
	w.overhead = span(top)
	w.bottoms = span(bottom)
	if len(hold) > 0 {
		w.holdupFirst = hold[0].Value
		w.holdupLast = hold[len(hold)-1].Value
		w.netAccumulation = w.holdupLast - w.holdupFirst
	} else {
		w.holdupFirst, w.holdupLast, w.netAccumulation = 0, 0, 0
	}
	if len(feed) < 2 {
		w.missing = appendMissing(w.missing, FeedIn)
	}
	if len(top) < 2 {
		w.missing = appendMissing(w.missing, Overhead)
	}
	if len(bottom) < 2 {
		w.missing = appendMissing(w.missing, Bottoms)
	}
	if len(hold) < 2 {
		w.missing = appendMissing(w.missing, Holdup)
	}
	w.lossLive = w.received - w.overhead - w.bottoms - w.netAccumulation
}

// span 返回累计表首末读数之差。
func span(rs []Reading) float64 {
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1].Value - rs[0].Value
}

func appendMissing(points []Point, p Point) []Point {
	for _, existing := range points {
		if existing == p {
			return points
		}
	}
	return append(points, p)
}

func nearZero(v float64) bool { return v >= -tolerance && v <= tolerance }

func (w *window) currentVerdict() string {
	if len(w.missing) > 0 {
		return VerdictIncomplete
	}
	switch {
	case nearZero(w.lossLive):
		return VerdictBalanced
	case w.lossLive > 0:
		return VerdictLoss
	default:
		return VerdictGain
	}
}

func (w *window) currentLoss() float64 { return w.lossLive }

// snapshot 产出对外的衡算结果。
func (w *window) snapshot() Balance {
	verdict := w.currentVerdict()
	loss := w.lossLive
	originalVerdict, originalLoss := verdict, loss
	conclusionHeld := true
	if w.hasVerdict {
		originalVerdict = w.verdict
		originalLoss = w.loss
		conclusionHeld = originalVerdict == verdict
	}
	return Balance{
		Window:          w.id,
		Reason:          w.reason,
		ClosedReason:    w.closedReason,
		Revision:        w.revision,
		FeedSource:      w.feedSource,
		FeedRevision:    w.feedRevision,
		StartAt:         w.startAt,
		EndAt:           w.endAt,
		Closed:          w.closed,
		Received:        w.received,
		Overhead:        w.overhead,
		Bottoms:         w.bottoms,
		HoldupFirst:     w.holdupFirst,
		HoldupLast:      w.holdupLast,
		NetAccumulation: w.netAccumulation,
		Loss:            loss,
		Missing:         append([]Point(nil), w.missing...),
		ReadingCount:    len(w.readings),
		Verdict:         verdict,
		OriginalVerdict: originalVerdict,
		OriginalLoss:    originalLoss,
		ConclusionHeld:  conclusionHeld,
		Amended:         len(w.amendments) > 0,
		Amendments:      append([]Amendment(nil), w.amendments...),
	}
}
