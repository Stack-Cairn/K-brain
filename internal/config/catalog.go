package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const catalogTTL = 24 * time.Hour

type Catalog struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	BaseURL   string          `json:"baseUrl"`
	Models    []ModelInfoLite `json:"models"`
}

type ModelInfoLite struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"contextLength,omitempty"`
	MaxCompletionTokens int      `json:"maxCompletionTokens,omitempty"`
	ReasoningEfforts    []string `json:"reasoningEfforts,omitempty"`
	InPrice             float64  `json:"inPrice,omitempty"`
	OutPrice            float64  `json:"outPrice,omitempty"`
	CacheReadPrice      float64  `json:"cacheReadPrice,omitempty"`
	InputModalities     []string `json:"inputModalities,omitempty"`
}

func (c Catalog) SupportsVision(id string) (vision, found bool) {
	for _, mi := range c.Models {
		if mi.ID == id {
			if len(mi.InputModalities) == 0 {
				return false, false
			}
			if slices.Contains(mi.InputModalities, "image") {
				return true, true
			}
			return false, true
		}
	}
	return false, false
}

func (c Catalog) ContextLength(id string) int {
	for _, mi := range c.Models {
		if mi.ID == id {
			return mi.ContextLength
		}
	}
	return 0
}

func (c Catalog) MaxCompletionTokens(id string) int {
	for _, mi := range c.Models {
		if mi.ID == id {
			return mi.MaxCompletionTokens
		}
	}
	return 0
}

func (c Catalog) Pricing(id string) (in, out, cacheRead float64, ok bool) {
	for _, mi := range c.Models {
		if mi.ID == id {
			return mi.InPrice, mi.OutPrice, mi.CacheReadPrice, mi.InPrice > 0 || mi.OutPrice > 0
		}
	}
	return 0, 0, 0, false
}

func (c Catalog) Find(id string) *ModelInfoLite {
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	return nil
}

func (c Catalog) Efforts(id string) []string {
	mi := c.Find(id)
	if mi == nil || len(mi.ReasoningEfforts) == 0 {
		return []string{""}
	}
	out := []string{""}
	for _, e := range mi.ReasoningEfforts {
		if e != "none" {
			out = append(out, e)
		}
	}
	return out
}

func catalogPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models.json"), nil
}

func LoadCatalogs() map[string]Catalog {
	cats := map[string]Catalog{}
	p, err := catalogPath()
	if err != nil {
		return cats
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return cats
	}
	if json.Unmarshal(data, &cats) != nil || cats == nil {
		return map[string]Catalog{}
	}
	return cats
}

func SaveCatalogs(cats map[string]Catalog) error {
	p, err := catalogPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cats, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

func (c Catalog) Stale() bool { return time.Since(c.FetchedAt) > catalogTTL }
