package materialbalance

import "github.com/google/uuid"

// Point 标识一个计量点。feed/overhead/bottoms 是累积式流量计（totalizer，
// 取窗口内首尾差）；inventory 是瞬时设备存量计（取期末与期初之差）。
type Point string

const (
	PointFeed      Point = "feed"      // 接收进料（累积）
	PointOverhead  Point = "overhead"  // 塔顶产出（累积）
	PointBottoms   Point = "bottoms"   // 塔底产出（累积）
	PointInventory Point = "inventory" // 设备存量（瞬时）
)

// 衡算结论。
const (
	VerdictBalanced   = "balanced"   // 损失在容差内
	VerdictLoss       = "loss"       // 投入 > 产出 + 净积累（物料缺失）
	VerdictGain       = "gain"       // 投入 < 产出 + 净积累（物料盈余）
	VerdictIncomplete = "incomplete" // 计量点不全，暂不能下结论
)

// 修订原因。
const (
	ReasonLateReading      = "late-reading"
	ReasonMissingInventory = "missing-closing-inventory"
)

// Reading 是一次计量点读数。At 为 unix 秒，与 model.Sample.At 对齐。
type Reading struct {
	Point Point   `json:"point"`
	Value float64 `json:"value"`
	At    int64   `json:"at"`
}

// Meta 标识一个核算窗口（时段）。窗口与单一进料源/进料修订绑定。
type Meta struct {
	Sequence     int       `json:"sequence"`
	CampaignID   uuid.UUID `json:"campaign_id"`
	Revision     uuid.UUID `json:"campaign_revision"`
	FeedRevision uuid.UUID `json:"feed_revision"`
	Source       string    `json:"source"`
	OpenedAt     int64     `json:"opened_at"`
	ClosedAt     int64     `json:"closed_at"` // 窗口打开期间为 0
	Settled      bool      `json:"settled"`
}

// Balance 是一个窗口在某一时刻的衡算结果。
type Balance struct {
	TotalInput     float64 `json:"total_input"`
	OverheadOut    float64 `json:"overhead_out"`
	BottomsOut     float64 `json:"bottoms_out"`
	TotalOutput    float64 `json:"total_output"`
	StartInventory float64 `json:"start_inventory"`
	EndInventory   float64 `json:"end_inventory"`
	InventoryKnown bool    `json:"inventory_known"`
	Accumulation   float64 `json:"accumulation"`
	Loss           float64 `json:"loss"`
	Verdict        string  `json:"verdict"`
	Tolerance      float64 `json:"tolerance"`
	Complete       bool    `json:"complete"`
	MissingPoints  []Point `json:"missing_points,omitempty"`
}

// Revision 是一次迟到读数引发的追加式修订。Original 在窗口首次结算时冻结，
// 永不被覆盖；每次迟到读数追加一条，Revised 为重算结果。
type Revision struct {
	Sequence int     `json:"sequence"`
	At       int64   `json:"revised_at"`
	Reason   string  `json:"reason"`
	Reading  Reading `json:"reading"`
	Original Balance `json:"original"`
	Revised  Balance `json:"revised"`
}

// WindowView 是一个窗口对外的只读视图。
type WindowView struct {
	Meta        Meta       `json:"meta"`
	Original    *Balance   `json:"original,omitempty"` // 结算时冻结；打开窗口为 nil
	Current     Balance    `json:"current"`
	Provisional bool       `json:"provisional"` // 缺窗内期末存量，沿用最近已知
	Revisions   []Revision `json:"revisions"`
}

// Acknowledgement 是读数被接受后的回执；Revised 表示它修订了一个已结算窗口。
type Acknowledgement struct {
	Sequence int  `json:"sequence"`
	Revised  bool `json:"revised"`
	Settled  bool `json:"settled"`
}
