// playbook.go 灵感案例（桌面端发现页）的数据。官方 5.8.3 渲染层从静态站点拉，不用登录
// （getDiscoverBaseUrl：https://static.workbuddy.cn/workbuddy/playbook）：
//
//	GET /registry.json           案例索引（cases[]）
//	GET /categories.json         分类（categories[]：id、name）
//	GET /cases/<id>/case.json    案例详情，打开详情页时拉
//
// 任务中心的「灵感案例做同款」只上报事件、不真发对话（用户 10-20 定），事件里的案例字段取这里
// 的真实值：类型 artifact_type、第一个分类及其名字、提示词长度。
package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

const defaultPlaybookBase = "https://static.workbuddy.cn/workbuddy/playbook"

// staticCacheTTL 官方静态站点的内容在网关里缓存多久。灵感案例索引有 6 MB 多（10-20 生产实测），
// 任务中心一轮里每个账号都要用；官方客户端也是拉一次用一阵。只缓存成功的结果。
const staticCacheTTL = time.Hour

// staticCache 静态站点内容的缓存：挑出的灵感案例、首页模板列表。拉取时持锁，并发的几个账号
// 只下载一次。
type staticCache struct {
	mu         sync.Mutex
	playbook   PlaybookCase
	playbookAt time.Time
	scenes     []HomeScene
	scenesAt   time.Time
}

func cacheFresh(at time.Time) bool { return !at.IsZero() && time.Since(at) < staticCacheTTL }

// PlaybookCase 网关挑出的一个灵感案例，字段对应官方 playbookMeta。
type PlaybookCase struct {
	ID           string
	Title        string
	Type         string // artifact_type，没有时 "other"
	CategoryID   string // 第一个分类
	CategoryName string
	PromptLength int // 中文提示词的 JS 长度（playbookPromptLength）
	Position     int // 在这个分类的列表里排第几（从 0 起）
}

// playbookEntry 案例索引和详情里网关用到的字段。
type playbookEntry struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	ArtifactType string            `json:"artifact_type"`
	Categories   []string          `json:"categories"`
	Experts      []json.RawMessage `json:"experts"`
	Skills       []json.RawMessage `json:"skills"`
	MCPs         []json.RawMessage `json:"mcps"`
	ScenarioID   json.RawMessage   `json:"scenario_id"`
	ScenarioUse  string            `json:"scenario_use"`
	Tags         []string          `json:"tags"`
	Prompt       json.RawMessage   `json:"prompt"`
}

// plain 不带专家、技能、MCP、场景模板的案例：官方做同款时只发提示词，不另召唤专家、不挂技能，
// 和网关只上报事件的做法对得上。Genie 案例走另一套流程，也排除。
func (e playbookEntry) plain() bool {
	return len(e.Experts) == 0 && len(e.Skills) == 0 && len(e.MCPs) == 0 &&
		jsonNull(e.ScenarioID) && e.ScenarioUse == "" && !slices.Contains(e.Tags, "Genie") && len(e.Categories) > 0
}

// promptZH 官方 resolveCasePrompt(e,"zh") → getLocalizedText：字符串原样，对象取 zh，没有取 en。
func (e playbookEntry) promptZH() string {
	var s string
	if json.Unmarshal(e.Prompt, &s) == nil {
		return s
	}
	var m map[string]string
	if json.Unmarshal(e.Prompt, &m) == nil {
		if v, ok := m["zh"]; ok {
			return v
		}
		return m["en"]
	}
	return ""
}

func jsonNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func (c *Client) playbookBase() string {
	if c.PlaybookBase != "" {
		return c.PlaybookBase
	}
	return defaultPlaybookBase
}

// playbookGet 拉灵感案例站点上的一个 JSON 文件。
func (c *Client) playbookGet(path string, out any) error {
	return c.staticGet(c.playbookBase()+path, "playbook "+path, out)
}

// staticGet 拉官方静态站点上的一个公开 JSON 文件（灵感案例、首页模板），不用登录。官方是 Electron
// 渲染进程里的 fetch；静态站点不认人，用网关的网页 UA。label 用在错误信息里。
func (c *Client) staticGet(url, label string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.webUserAgent())
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", acceptEncodingChrome)
	resp, err := doDecoded(c.HTTP, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d", label, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s parse: %w", label, err)
	}
	return nil
}

// PickPlaybookCase 照官方发现页的顺序拉索引和分类，挑索引里第一个 plain 的案例，再拉它的详情。
// Position 是它在第一个分类的列表里（索引顺序）的位置。结果缓存 staticCacheTTL。账号参数留给以后
// 按域区分，现在两域同一站点。
func (c *Client) PickPlaybookCase(_ *auth.Auth) (PlaybookCase, error) {
	c.static.mu.Lock()
	defer c.static.mu.Unlock()
	if cacheFresh(c.static.playbookAt) {
		return c.static.playbook, nil
	}
	pc, err := c.pickPlaybookCase()
	if err != nil {
		return PlaybookCase{}, err
	}
	c.static.playbook, c.static.playbookAt = pc, time.Now()
	return pc, nil
}

func (c *Client) pickPlaybookCase() (PlaybookCase, error) {
	var reg struct {
		Cases []playbookEntry `json:"cases"`
	}
	if err := c.playbookGet("/registry.json", &reg); err != nil {
		return PlaybookCase{}, err
	}
	var cats struct {
		Categories []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"categories"`
	}
	if err := c.playbookGet("/categories.json", &cats); err != nil {
		return PlaybookCase{}, err
	}
	idx := slices.IndexFunc(reg.Cases, playbookEntry.plain)
	if idx < 0 {
		return PlaybookCase{}, fmt.Errorf("没有不带专家和技能的灵感案例")
	}
	pick := reg.Cases[idx]
	var detail playbookEntry
	if err := c.playbookGet("/cases/"+pick.ID+"/case.json", &detail); err != nil {
		return PlaybookCase{}, err
	}
	if detail.ID == "" {
		detail = pick
	}
	pc := PlaybookCase{
		ID: detail.ID, Title: detail.Title, Type: detail.ArtifactType,
		PromptLength: jsLength(detail.promptZH()),
	}
	if pc.Type == "" {
		pc.Type = "other"
	}
	if len(detail.Categories) > 0 {
		pc.CategoryID = detail.Categories[0]
	}
	for _, cat := range cats.Categories {
		if cat.ID == pc.CategoryID {
			pc.CategoryName = cat.Name
			break
		}
	}
	for _, e := range reg.Cases[:idx] {
		if slices.Contains(e.Categories, pc.CategoryID) {
			pc.Position++
		}
	}
	return pc, nil
}
