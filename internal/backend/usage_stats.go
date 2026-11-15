package backend

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// GET /v1/usage/stats?days=N&tz=IANA（capabilities.usage_stats）：所有工作区会话的用量统计。
// 统计口径见 internal/session/usage_stats.go；这里解析参数并按模型目录计价。

const (
	usageStatsDefaultDays = 371 // 52 周加几天余量，够画一整年的热力图
	usageStatsMaxDays     = 1100
)

type usageStatsTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
	Turns      int   `json:"turns"`
}

func newUsageStatsTokens(t session.UsageTokens) usageStatsTokens {
	return usageStatsTokens{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, Total: t.Total(), Turns: t.Turns}
}

type usageStatsTotals struct {
	usageStatsTokens
	Sessions int      `json:"sessions"`
	CostUSD  *float64 `json:"costUSD"`
}

type usageStatsSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Total int64  `json:"total"`
}

type usageStatsSummary struct {
	ActiveDays     int                `json:"activeDays"`
	CurrentStreak  int                `json:"currentStreak"`
	LongestStreak  int                `json:"longestStreak"`
	TopModel       string             `json:"topModel"`
	LongestSession *usageStatsSession `json:"longestSession"`
}

type usageStatsDay struct {
	Date string `json:"date"`
	usageStatsTokens
	Models map[string]int64 `json:"models"`
}

type usageStatsModel struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	usageStatsTokens
	CostUSD *float64 `json:"costUSD"`
}

type usageStatsResponse struct {
	GeneratedAt string            `json:"generatedAt"`
	Timezone    string            `json:"timezone"`
	Days        int               `json:"days"`
	Totals      usageStatsTotals  `json:"totals"`
	Summary     usageStatsSummary `json:"summary"`
	Daily       []usageStatsDay   `json:"daily"`
	Models      []usageStatsModel `json:"models"`
}

func (s *Server) handleUsageStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	days := usageStatsDefaultDays
	if raw := strings.TrimSpace(query.Get("days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "days must be an integer")
			return
		}
		days = min(max(n, 1), usageStatsMaxDays)
	}
	loc, zone := localZone()
	if raw := strings.TrimSpace(query.Get("tz")); raw != "" {
		named, err := time.LoadLocation(raw)
		if err != nil || raw == "Local" {
			writeJSONError(w, http.StatusBadRequest, "tz must be an IANA time zone name")
			return
		}
		loc, zone = named, raw
	}
	now := time.Now()
	stats, err := s.store.UsageStats(r.Context(), session.UsageStatsOptions{Days: days, Location: loc, Now: now})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var cfg *config.Config
	if s.settings != nil {
		cfg = s.settings.Snapshot()
	}
	writeJSON(w, http.StatusOK, buildUsageStatsResponse(stats, cfg, config.LoadCatalogs(), now.In(loc), zone, days))
}

func buildUsageStatsResponse(stats session.UsageStats, cfg *config.Config, cats map[string]config.Catalog, now time.Time, zone string, days int) usageStatsResponse {
	out := usageStatsResponse{
		GeneratedAt: now.Format(time.RFC3339),
		Timezone:    zone,
		Days:        days,
		Totals:      usageStatsTotals{usageStatsTokens: newUsageStatsTokens(stats.Totals), Sessions: stats.Sessions},
		Summary: usageStatsSummary{ActiveDays: stats.ActiveDays, CurrentStreak: stats.CurrentStreak,
			LongestStreak: stats.LongestStreak, TopModel: stats.TopModel},
		Daily:  make([]usageStatsDay, 0, len(stats.Daily)),
		Models: make([]usageStatsModel, 0, len(stats.Models)),
	}
	if ls := stats.LongestSession; ls != nil {
		out.Summary.LongestSession = &usageStatsSession{ID: ls.ID, Title: ls.Title, Total: ls.Total}
	}
	for _, day := range stats.Daily {
		out.Daily = append(out.Daily, usageStatsDay{Date: day.Date, usageStatsTokens: newUsageStatsTokens(day.UsageTokens), Models: day.Models})
	}
	// 费用与 kn 的 /usage 卡片同一套算法（config.UsageCost）；按订阅计费的 provider 不计价。
	// 总费用只加能计价的模型，一个都不能计价时为 null。
	var total float64
	priced := false
	for _, m := range stats.Models {
		entry := usageStatsModel{Model: m.Model, Provider: m.Provider, usageStatsTokens: newUsageStatsTokens(m.UsageTokens)}
		if !cfg.SubscriptionBilled(m.Provider) {
			if cost, ok := config.UsageCost(cats, m.Model, m.Provider, m.Usage()); ok && !math.IsInf(cost, 0) && !math.IsNaN(cost) {
				rounded := roundUSD(cost)
				entry.CostUSD = &rounded
				total += cost
				priced = true
			}
		}
		out.Models = append(out.Models, entry)
	}
	if priced {
		rounded := roundUSD(total)
		out.Totals.CostUSD = &rounded
	}
	return out
}

// roundUSD 保留到百万分之一美元，避免 0.30000000000000004 这样的浮点尾巴。
func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// localZone 是进程的本地时区及其 IANA 名称。TZ 或 /etc/localtime 都给不出名称时
// （例如 Windows），名称为 time.Local 的 "Local"。
func localZone() (*time.Location, string) {
	if tz, ok := os.LookupEnv("TZ"); ok {
		if tz == "" {
			return time.UTC, "UTC"
		}
		name := strings.TrimPrefix(tz, ":")
		if loc, err := time.LoadLocation(name); err == nil && name != "Local" {
			return loc, name
		}
	}
	if path, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(filepath.ToSlash(path), "zoneinfo/"); ok {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc, name
			}
		}
	}
	return time.Local, time.Local.String()
}
