// refresh_identity.go 刷新令牌后照官方合并账号、核对身份。
//
// 依据（10-20 读官方桌面端 5.8.3 内置 CLI 确认）：refreshSession 用新令牌取到账号后，
// mergeCurrentAccountDetails 从里面挑出本地账号对应的那个（uid 相同，再按类型与企业 id 挑），
// 把它的字段盖到本地账号上，但 uid、type 保留本地的，enterpriseId 本地有就用本地的；挑不出来
// 就保留本地账号。随后 normalizeAndAssertRefreshedAuthority 比对刷新前后的身份
// （getAuthenticationAuthorityKey：uid、type、enterpriseId、oneidAccountId、departmentFullName、
// idp 与令牌的 tokenType、domain，缺这个键和空串算不同的值；本地有 idp 而合并后没有时沿用本地
// 的），有任何一项变了就当刷新失败，要重新登录。
//
// 网关的差别只有一条：凭证里没有账号类型（1.1.2 以前的网关写的，账号接口恒有 type）时，挑账号
// 不看类型，合并后只补字段、不比对——这些字段以前没存过，没法知道变没变。
package upstream

import (
	"encoding/json"
	"strings"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// refreshAccountJSON 取账号响应里的一个账号。指针为 nil 表示没有这个键。
type refreshAccountJSON struct {
	UID                string          `json:"uid"`
	Type               *string         `json:"type"`
	EnterpriseID       *string         `json:"enterpriseId"`
	Nickname           *string         `json:"nickname"`
	DepartmentFullName *string         `json:"departmentFullName"`
	OneIDAccountID     *string         `json:"oneidAccountId"`
	IDP                *string         `json:"idp"`
	LastLogin          json.RawMessage `json:"lastLogin"`
}

// personalEditionTypes 官方 isPersonalEditionAccount 里不算个人版的账号类型。
var personalEditionTypes = map[string]bool{"ultimate": true, "exclusive": true}

// isPersonalEdition 官方 isPersonalEditionAccount：没有企业 id，且类型不是 ultimate / exclusive。
func isPersonalEdition(enterpriseID, accountType string) bool {
	if strings.TrimSpace(enterpriseID) != "" {
		return false
	}
	return !personalEditionTypes[strings.ToLower(strings.TrimSpace(accountType))]
}

// matchRefreshedAccount 照官方 mergeCurrentAccountDetails 挑本地账号 old 对应的那个：uid 相同的
// 账号里，类型与企业 id 都相同的恰好一个；否则本地有企业 id 时企业 id 相同的恰好一个；否则
// （本地没有企业 id）lastLogin 为真、个人版与否和本地一样的恰好一个。都不成立返回 nil。本地
// 没有 uid 时一个都对不上；本地没有账号类型时不比类型（见文件头）。
func matchRefreshedAccount(accounts []refreshAccountJSON, old *auth.Auth) *refreshAccountJSON {
	if old.UID == "" {
		return nil
	}
	ent := strings.TrimSpace(old.EnterpriseID)
	oldType := strings.ToLower(strings.TrimSpace(derefStr(old.AccountType)))
	oldPersonal := isPersonalEdition(ent, oldType)
	var same, byEnt, byLast []*refreshAccountJSON
	for i := range accounts {
		acc := &accounts[i]
		if acc.UID != old.UID {
			continue
		}
		accEnt := strings.TrimSpace(derefStr(acc.EnterpriseID))
		accType := strings.ToLower(strings.TrimSpace(derefStr(acc.Type)))
		if (old.AccountType == nil || accType == oldType) && accEnt == ent {
			same = append(same, acc)
		}
		if ent != "" && accEnt == ent {
			byEnt = append(byEnt, acc)
		}
		if ent == "" && jsonTruthy(acc.LastLogin) && isPersonalEdition(accEnt, accType) == oldPersonal {
			byLast = append(byLast, acc)
		}
	}
	for _, set := range [][]*refreshAccountJSON{same, byEnt, byLast} {
		if len(set) == 1 {
			return set[0]
		}
	}
	return nil
}

// mergedAccount 合并后的账号里网关存的字段。
type mergedAccount struct {
	nickname       string
	department     *string
	accountType    *string
	oneIDAccountID *string
	idp            *string
}

// mergeRefreshedAccount 照官方把挑出的账号 hit 盖到本地账号 old 上：hit 里有哪个键盖哪个，type
// 保留本地的（本地没有时取 hit 的）；本地有 idp 而合并后为空时沿用本地的。hit 为 nil 时就是本地
// 账号。昵称只认非空值，同 Pool.SetNickname（防脏数据把面板上的名字清掉）。
func mergeRefreshedAccount(old *auth.Auth, hit *refreshAccountJSON) mergedAccount {
	m := mergedAccount{nickname: old.Nickname, accountType: old.AccountType,
		oneIDAccountID: old.OneIDAccountID, idp: old.IDP}
	if old.DepartmentSet || old.DepartmentFullName != "" {
		d := old.DepartmentFullName
		m.department = &d
	}
	if hit == nil {
		return m
	}
	if hit.Nickname != nil && *hit.Nickname != "" {
		m.nickname = *hit.Nickname
	}
	if hit.DepartmentFullName != nil {
		m.department = hit.DepartmentFullName
	}
	if hit.OneIDAccountID != nil {
		m.oneIDAccountID = hit.OneIDAccountID
	}
	if hit.IDP != nil {
		m.idp = hit.IDP
	}
	if derefStr(old.IDP) != "" && derefStr(m.idp) == "" {
		m.idp = old.IDP
	}
	if m.accountType == nil {
		m.accountType = hit.Type
	}
	return m
}

// refreshIdentityKept 官方 normalizeAndAssertRefreshedAuthority：刷新前（old）与合并后（m 加新令牌的
// tokenType、domain）的身份一致时为真。uid、enterpriseId 合并时不会变，不比。本地没有账号
// 类型时不比对，恒为真（见文件头）。本地登录域为空按没有这个键算。
func refreshIdentityKept(old *auth.Auth, m mergedAccount, tokenType, domain *string) bool {
	if old.AccountType == nil {
		return true
	}
	var oldDept, oldDomain *string
	if old.DepartmentSet || old.DepartmentFullName != "" {
		d := old.DepartmentFullName
		oldDept = &d
	}
	if old.Domain != "" {
		d := old.Domain
		oldDomain = &d
	}
	pairs := [][2]*string{
		{old.AccountType, m.accountType},
		{old.OneIDAccountID, m.oneIDAccountID},
		{oldDept, m.department},
		{old.IDP, m.idp},
		{old.TokenType, tokenType},
		{oldDomain, domain},
	}
	for _, p := range pairs {
		if (p[0] == nil) != (p[1] == nil) || (p[0] != nil && *p[0] != *p[1]) {
			return false
		}
	}
	return true
}

// applyTo 把合并结果写回凭证。调用方持有 a 的锁；值没变的不写（昵称有不加锁的读取方，同
// SetNickname）。
func (m mergedAccount) applyTo(a *auth.Auth) {
	if m.nickname != "" && m.nickname != a.Nickname {
		a.Nickname = m.nickname
	}
	if m.department != nil && (*m.department != a.DepartmentFullName || !a.DepartmentSet) {
		a.DepartmentFullName, a.DepartmentSet = *m.department, true
	}
	a.AccountType, a.OneIDAccountID, a.IDP = m.accountType, m.oneIDAccountID, m.idp
}

// derefStr 取指针的值，nil 为空串。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// jsonTruthy JS 的真值判断：缺失、null、false、0、空串为假。
func jsonTruthy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`:
		return false
	}
	return true
}
