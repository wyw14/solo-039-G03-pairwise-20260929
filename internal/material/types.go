package material

import (
	"errors"
	"math"

	"github.com/google/uuid"
)

// Point 是物料衡算的计量点。feed-in 为接收进料的总量表，
// overhead/bottoms 为塔顶、塔底产出表，holdup 为设备存量（液位）。
type Point string

const (
	FeedIn   Point = "feed-in"
	Overhead Point = "overhead"
	Bottoms  Point = "bottoms"
	Holdup   Point = "holdup"
)

// 衡算窗口被切开的原因。
const (
	ReasonStart      = "start"
	ReasonFeedSwitch = "feed-switch"
	ReasonShutdown   = "shutdown"
)

// 衡算结论。
const (
	VerdictBalanced   = "balanced"
	VerdictLoss       = "loss"
	VerdictGain       = "gain"
	VerdictIncomplete = "incomplete"
)

// Reading 是一次计量点读数。Seq 由台账统一编号，At 为测量时刻，
// Recorded 为读数到达台账的时刻，迟到读数凭 At 归入受影响时段。
type Reading struct {
	Seq          int64
	Point        Point
	Value        float64
	At           int64
	Recorded     int64
	FeedRevision uuid.UUID
	Window       string
}

func (r Reading) Validate() error {
	switch r.Point {
	case FeedIn, Overhead, Bottoms, Holdup:
	default:
		return errors.New("unknown material metering point")
	}
	if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 {
		return errors.New("meter reading is not a non-negative number")
	}
	if r.At <= 0 {
		return errors.New("meter reading timestamp is required")
	}
	return nil
}

// ParsePoint 把外部传入的计量点名称解析为 Point。
func ParsePoint(value string) (Point, error) {
	switch Point(value) {
	case FeedIn, Overhead, Bottoms, Holdup:
		return Point(value), nil
	default:
		return "", errors.New("unknown material metering point")
	}
}

// Amendment 记录一次对已结账窗口的修订：迟到读数补录或既有读数更正。
// 窗口关闭时冻结的原结论保留在 Balance.OriginalVerdict/OriginalLoss，
// 修订只追加痕迹并重算当期数值，不覆盖原结论。
type Amendment struct {
	Seq             int64
	Point           Point
	At              int64
	Recorded        int64
	Reason          string
	PreviousLoss    float64
	PreviousVerdict string
	RevisedLoss     float64
	RevisedVerdict  string
}

// Balance 是一个衡算窗口按时段核算的结果。
//
// 各计量点读数视为累计总量（存量点为设备持有量），时段量取末读数-首读数。
// 物料衡算：接收进料 = 塔顶产出 + 塔底产出 + 设备净积累 + 损失，
// 因此 Loss = Received - Overhead - Bottoms - NetAccumulation。
type Balance struct {
	Window          string
	Reason          string
	ClosedReason    string
	Revision        uuid.UUID
	FeedSource      string
	FeedRevision    uuid.UUID
	StartAt         int64
	EndAt           int64
	Closed          bool
	Received        float64
	Overhead        float64
	Bottoms         float64
	HoldupFirst     float64
	HoldupLast      float64
	NetAccumulation float64
	Loss            float64
	Missing         []Point
	ReadingCount    int
	Verdict         string
	// Original* 是窗口关闭时冻结的原结论；修订后仍然保留。
	OriginalVerdict string
	OriginalLoss    float64
	ConclusionHeld  bool
	Amended         bool
	Amendments      []Amendment
}
