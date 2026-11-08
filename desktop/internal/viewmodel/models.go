package viewmodel

import (
	"gammagrid.local/desktop/internal/marketclock"
)

type TickerSummary struct {
	Ribbon          *RibbonData `json:"ribbon"`
	Symbol          string      `json:"symbol"`
	Source          string      `json:"source"`
	Since           string      `json:"since"`
	HasData         bool        `json:"has_data"`
	Spot            *float64    `json:"spot"`
	NetGEX          *float64    `json:"net_gex"`
	NetGEXStr       string      `json:"net_gex_str"`
	State           string      `json:"state"`
	StateCN         string      `json:"state_cn"`
	CollectedAt     string      `json:"collected_at"`
	LowFreqEnabled  bool        `json:"low_freq_enabled"`
	HighFreqEnabled bool        `json:"high_freq_enabled"`
	HighFreqSec     int         `json:"high_freq_sec"`
	HighFreqMinutes int
	HighFreqUntil   string
}

type IntervalChoice struct {
	Label   string `json:"label"`
	Minutes int    `json:"minutes"`
}

type RunItem struct {
	StartedAt string `json:"started_at"`
	Ticker    string `json:"ticker"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Rows      int    `json:"rows"`
	OIZero    string `json:"oi_zero"`
	Error     string `json:"error"`
}

type AppStateResponse struct {
	Schedule         marketclock.ScheduleConfig `json:"schedule"`
	RefreshMode      string                     `json:"refresh_mode"`
	NextCollection   string                     `json:"next_collection"`
	Version          string                     `json:"version"`
	Watchlist        []string                   `json:"watchlist"`
	Tickers          []TickerSummary            `json:"tickers"`
	CollectorState   string                     `json:"collector_state"`
	CollectorError   string                     `json:"collector_error"`
	IntervalMinutes  int                        `json:"interval_minutes"`
	IntervalChoices  []IntervalChoice           `json:"interval_choices"`
	GrowthGBPerMonth float64                    `json:"growth_gb_per_month"`
	Cooldowns        map[string]string          `json:"cooldowns"`
	Runs             []RunItem                  `json:"runs"`
}

type RibbonData struct {
	Spot         float64  `json:"spot"`
	SpotStr      string   `json:"spot_str"`
	State        string   `json:"state"`
	StateCN      string   `json:"state_cn"`
	StateSub     string   `json:"state_sub"`
	Positive     bool     `json:"positive"`
	Scope        string   `json:"scope"`
	NetGEX       *float64 `json:"net_gex"`
	NetGEXStr    string   `json:"net_gex_str"`
	GammaFlip    *float64 `json:"gamma_flip"`
	GammaFlipStr string   `json:"gamma_flip_str"`
	FlipDistStr  string   `json:"flip_dist_str"`
	CallGEXWall  string   `json:"call_gex_wall"`
	PutGEXWall   string   `json:"put_gex_wall"`
	CallOIWall   string   `json:"call_oi_wall"`
	PutOIWall    string   `json:"put_oi_wall"`
	MaxPain      string   `json:"max_pain"`
	MaxPainExp   string   `json:"max_pain_exp"`
	MoveStr      string   `json:"move_str"`
	MovePctStr   string   `json:"move_pct_str"`
	ATMIVStr     string   `json:"atm_iv_str"`
	PCROIStr     string   `json:"pcr_oi_str"`
	PCRVolStr    string   `json:"pcr_vol_str"`
}

type MomentOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type ExpiryBoardRow struct {
	Expiry     string  `json:"expiry"`
	ExpiryISO  string  `json:"expiry_iso"`
	DTE        int     `json:"dte"`
	NetGEX     float64 `json:"net_gex"`
	NetGEXStr  string  `json:"net_gex_str"`
	CallGEXStr string  `json:"call_gex_str"`
	PutGEXStr  string  `json:"put_gex_str"`
	MaxPain    string  `json:"max_pain"`
	ATMIV      string  `json:"atm_iv"`
	Move       string  `json:"move"`
	MovePct    string  `json:"move_pct"`
	CallOI     string  `json:"call_oi"`
	PutOI      string  `json:"put_oi"`
	PCR        string  `json:"pcr"`
	Contracts  int     `json:"contracts"`
}

type WorkbenchResponse struct {
	Ticker         string           `json:"ticker"`
	Source         string           `json:"source"`
	Empty          bool             `json:"empty"`
	CollectedAt    string           `json:"collected_at"`
	AsOf           string           `json:"as_of"`
	AsOfDisplay    string           `json:"as_of_display"`
	Moments        []MomentOption   `json:"moments"`
	ContractsCount int              `json:"contracts_count"`
	Dot            string           `json:"dot"`
	StatusNote     string           `json:"status_note"`
	Ribbon         RibbonData       `json:"ribbon"`
	Dimension      string           `json:"dimension"`
	DimensionCN    string           `json:"dimension_cn"`
	UnitCN         string           `json:"unit_cn"`
	Side           string           `json:"side"`
	MaxExpiries    int              `json:"max_expiries"`
	ShownExpiries  int              `json:"shown_expiries"`
	Spot           float64          `json:"spot"`
	StrikeRangePct float64          `json:"strike_range_pct,omitempty"`
	StrikeLower    *float64         `json:"strike_lower,omitempty"`
	StrikeUpper    *float64         `json:"strike_upper,omitempty"`
	ATMStrike      *float64         `json:"atm_strike"`
	Flip           *float64         `json:"flip"`
	MoveLower      *float64         `json:"move_lower"`
	MoveUpper      *float64         `json:"move_upper"`
	Strikes        []float64        `json:"strikes"`
	StrikeLabels   []string         `json:"strike_labels"`
	StrikeTags     []string         `json:"strike_tags"`
	Expiries       []string         `json:"expiries"`
	ExpiryDTEs     []string         `json:"expiry_dtes"`
	Totals         []float64        `json:"totals"`
	TotalsStr      []string         `json:"totals_str"`
	GrandTotal     float64          `json:"grand_total"`
	GrandTotalStr  string           `json:"grand_total_str"`
	CellsText      [][]string       `json:"cells_text"`
	CellsNorm      [][]float64      `json:"cells_norm"`
	CellsTip       [][]string       `json:"cells_tip"`
	Profile        []float64        `json:"profile"`
	ProfileStr     []string         `json:"profile_str"`
	ProfileMax     float64          `json:"profile_max"`
	CallProfile    []float64        `json:"call_profile"`
	CallProfileStr []string         `json:"call_profile_str"`
	PutProfile     []float64        `json:"put_profile"`
	PutProfileStr  []string         `json:"put_profile_str"`
	StrikeCallOI   []string         `json:"strike_call_oi"`
	StrikePutOI    []string         `json:"strike_put_oi"`
	Intraday       IntradaySummary  `json:"intraday"`
	Board          []ExpiryBoardRow `json:"board"`
}

type SkewCurve struct {
	Label   string    `json:"label"`
	Strikes []float64 `json:"strikes"`
	IVs     []float64 `json:"ivs"`
}

type TermPoint struct {
	Label   string  `json:"label"`
	DTE     int     `json:"dte"`
	ATMIV   float64 `json:"atm_iv"`
	MovePct float64 `json:"move_pct"`
	Move    float64 `json:"move"`
}

type IVHistoryPoint struct {
	Time string  `json:"time"`
	IV   float64 `json:"iv"`
}

type RVItem struct {
	Window string  `json:"window"`
	Value  string  `json:"value"`
	Raw    float64 `json:"raw"`
}

type VolatilityResponse struct {
	Ticker    string           `json:"ticker"`
	Empty     bool             `json:"empty"`
	Spot      float64          `json:"spot"`
	Skew      []SkewCurve      `json:"skew"`
	Term      []TermPoint      `json:"term"`
	IVHistory []IVHistoryPoint `json:"iv_history"`
	RV        []RVItem         `json:"rv"`
}

type ExpiryOption struct {
	ISO   string `json:"iso"`
	Label string `json:"label"`
}

type PinnedItem struct {
	ID        int     `json:"id"`
	ExpiryISO string  `json:"expiry_iso"`
	Strike    float64 `json:"strike"`
	Type      string  `json:"type"`
	Label     string  `json:"label"`
}

type ChainRow struct {
	ExpiryISO string  `json:"expiry_iso"`
	Expiry    string  `json:"expiry"`
	DTE       int     `json:"dte"`
	Strike    float64 `json:"strike"`
	StrikeStr string  `json:"strike_str"`
	Type      string  `json:"type"`
	TypeShort string  `json:"type_short"`
	Moneyness string  `json:"moneyness"`
	Bid       string  `json:"bid"`
	Ask       string  `json:"ask"`
	Last      string  `json:"last"`
	Volume    string  `json:"volume"`
	OI        string  `json:"oi"`
	IV        string  `json:"iv"`
	Delta     string  `json:"delta"`
	Gamma     string  `json:"gamma"`
	Theta     string  `json:"theta"`
	Vega      string  `json:"vega"`
	Vanna     string  `json:"vanna"`
	Charm     string  `json:"charm"`
}

type ChainResponse struct {
	Page          int            `json:"page"`
	PageSize      int            `json:"page_size"`
	Ticker        string         `json:"ticker"`
	Empty         bool           `json:"empty"`
	Spot          float64        `json:"spot"`
	TotalChain    int            `json:"total_chain"`
	TotalMatching int            `json:"total_matching"`
	Expiries      []ExpiryOption `json:"expiries"`
	Pinned        []PinnedItem   `json:"pinned"`
	Rows          []ChainRow     `json:"rows"`
}

type AttributionStep struct {
	Label string  `json:"label"`
	Cents float64 `json:"cents"`
	Str   string  `json:"str"`
	Kind  string  `json:"kind"`
}

type ContractPoint struct {
	Time  string   `json:"time"`
	Price *float64 `json:"price"`
	IV    *float64 `json:"iv"`
	Delta *float64 `json:"delta"`
	Gamma *float64 `json:"gamma"`
	Theta *float64 `json:"theta"`
	Vega  *float64 `json:"vega"`
}

type ContractResponse struct {
	Empty       bool              `json:"empty"`
	Name        string            `json:"name"`
	ExpiryISO   string            `json:"expiry_iso"`
	Strike      float64           `json:"strike"`
	Type        string            `json:"type"`
	PriceStr    string            `json:"price_str"`
	IVStr       string            `json:"iv_str"`
	DeltaStr    string            `json:"delta_str"`
	GammaStr    string            `json:"gamma_str"`
	ThetaStr    string            `json:"theta_str"`
	VegaStr     string            `json:"vega_str"`
	Samples     int               `json:"samples"`
	Attribution []AttributionStep `json:"attribution"`
	Points      []ContractPoint   `json:"points"`
}

type UnusualRow struct {
	Expiry string `json:"expiry"`
	Strike string `json:"strike"`
	Type   string `json:"type"`
	Volume string `json:"volume"`
	OI     string `json:"oi"`
	AvgVol string `json:"avg_vol"`
	ZScore string `json:"zscore"`
	VolOI  string `json:"vol_oi"`
}

type OIDeltaRow struct {
	Expiry     string `json:"expiry"`
	Strike     string `json:"strike"`
	Type       string `json:"type"`
	OI         string `json:"oi"`
	OIDelta    int    `json:"oi_delta"`
	OIDeltaStr string `json:"oi_delta_str"`
	OIPctStr   string `json:"oi_pct_str"`
}

type LadderRow struct {
	Label  string `json:"label"`
	Before string `json:"before"`
	After  string `json:"after"`
	Delta  string `json:"delta"`
	Tone   string `json:"tone"`
}

type PCRPoint struct {
	Time   string   `json:"time"`
	PCRVol *float64 `json:"pcr_vol"`
	PCROI  *float64 `json:"pcr_oi"`
}

type FlowResponse struct {
	Ticker     string       `json:"ticker"`
	Empty      bool         `json:"empty"`
	Spot       float64      `json:"spot"`
	Unusual    []UnusualRow `json:"unusual"`
	OIDelta    []OIDeltaRow `json:"oi_delta"`
	DaysCount  int          `json:"days_count"`
	DaysLabel  string       `json:"days_label"`
	Ladder     []LadderRow  `json:"ladder"`
	PCRHistory []PCRPoint   `json:"pcr_history"`
}

type IntradayBar struct {
	Time           string  `json:"time"`
	WindowLabel    string  `json:"window_label"`
	Spot           float64 `json:"spot"`
	CallVol        float64 `json:"call_vol"`
	PutVol         float64 `json:"put_vol"`
	TotalVol       float64 `json:"total_vol"`
	PCRatio        float64 `json:"pc_ratio"`
	CallPrem       float64 `json:"call_prem"`
	PutPrem        float64 `json:"put_prem"`
	TotalPrem      float64 `json:"total_prem"`
	NetPrem        float64 `json:"net_prem"`
	WeeklyCallPrem float64 `json:"weekly_call_prem"`
	WeeklyPutPrem  float64 `json:"weekly_put_prem"`
	WeeklyNetPrem  float64 `json:"weekly_net_prem"`
	CumWeeklyCall  float64 `json:"cum_weekly_call"`
	CumWeeklyPut   float64 `json:"cum_weekly_put"`
	CumWeeklyNet   float64 `json:"cum_weekly_net"`
	WeeklyNetGEX   float64 `json:"weekly_net_gex"`
	IsBaseline     bool    `json:"is_baseline"`
}

type IntradaySummary struct {
	DateLabel         string        `json:"date_label"`
	TotalVol          float64       `json:"total_vol"`
	TotalVolStr       string        `json:"total_vol_str"`
	TotalPrem         float64       `json:"total_prem"`
	TotalPremStr      string        `json:"total_prem_str"`
	NetPrem           float64       `json:"net_prem"`
	NetPremStr        string        `json:"net_prem_str"`
	PutVol            float64       `json:"put_vol"`
	PutVolStr         string        `json:"put_vol_str"`
	PutPrem           float64       `json:"put_prem"`
	PutPremStr        string        `json:"put_prem_str"`
	CallVol           float64       `json:"call_vol"`
	CallVolStr        string        `json:"call_vol_str"`
	CallPrem          float64       `json:"call_prem"`
	CallPremStr       string        `json:"call_prem_str"`
	WeeklyScope       string        `json:"weekly_scope"`
	WeeklyCallPremStr string        `json:"weekly_call_prem_str"`
	WeeklyPutPremStr  string        `json:"weekly_put_prem_str"`
	WeeklyNetPrem     float64       `json:"weekly_net_prem"`
	WeeklyNetPremStr  string        `json:"weekly_net_prem_str"`
	WeeklyNetGEX      float64       `json:"weekly_net_gex"`
	WeeklyNetGEXStr   string        `json:"weekly_net_gex_str"`
	TimeBars          []IntradayBar `json:"time_bars"`
	StrikeBars        []IntradayBar `json:"strike_bars"`
}
