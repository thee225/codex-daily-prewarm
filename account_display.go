package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type accountDisplayDetail struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type,omitempty"`
}

// Resolve only bounded display metadata. The opt-in cache is separate from
// anonymous execution records, audit events and notification deduplication IDs.
func loadAccountDisplayDetails() (map[string]accountDisplayDetail, error) {
	raw, err := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	var response authListResponse
	if err != nil || json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("account_details_unavailable")
	}
	details := make(map[string]accountDisplayDetail)
	for _, auth := range response.Files {
		if (strings.EqualFold(auth.Provider, "codex") || strings.EqualFold(auth.Type, "codex")) && strings.TrimSpace(auth.ID) != "" {
			name := strings.TrimSpace(auth.Label)
			if name == "" {
				name = strings.TrimSpace(auth.Email)
			}
			if name == "" {
				name = filepath.Base(strings.TrimSpace(auth.Name))
				if name == "." || name == "/" {
					name = ""
				}
			}
			details[accountFingerprint(auth.ID)] = accountDisplayDetail{Name: safeDisplayName(name), AccountType: accountDisplayPlan(auth)}
		}
	}
	return details, nil
}

func accountDisplayPlan(auth pluginapi.HostAuthFileEntry) string {
	if plan := safeAccountType(auth.AccountType); plan != "" {
		return plan
	}
	if strings.TrimSpace(auth.AuthIndex) == "" {
		return ""
	}
	// Some CPA versions expose "oauth" rather than the subscription plan in
	// account_type. Read only plan metadata via the existing local host callback.
	raw, err := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: auth.AuthIndex})
	if err != nil {
		return ""
	}
	var credential pluginapi.HostAuthGetResponse
	var metadata struct {
		PlanType string `json:"plan_type"`
	}
	if json.Unmarshal(raw, &credential) != nil || json.Unmarshal(credential.JSON, &metadata) != nil {
		return ""
	}
	return safeAccountType(metadata.PlanType)
}

// The presentation preference and metadata cache are shared across all displays.
// Routing, limits, notification deduplication and audit continue to use fingerprints.
type accountDisplayState struct {
	Enabled  bool                            `json:"enabled"`
	Accounts map[string]accountDisplayDetail `json:"accounts,omitempty"`
}

func safeDisplayName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	for len(name) > 128 {
		r := []rune(name)
		name = string(r[:len(r)-1])
	}
	return name
}
func cloneAccountDisplay(d accountDisplayState) accountDisplayState {
	clone := accountDisplayState{Enabled: d.Enabled}
	if d.Enabled {
		clone.Accounts = make(map[string]accountDisplayDetail, len(d.Accounts))
		for id, info := range d.Accounts {
			clone.Accounts[id] = info
		}
	}
	return clone
}
func (r *runtime) accountDisplay() accountDisplayState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneAccountDisplay(r.state.AccountDisplay)
}
func (r *runtime) setAccountDisplay(enabled bool) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.terminal {
		return errors.New("runtime_closed")
	}
	r.displayMu.Lock()
	defer r.displayMu.Unlock()
	next := accountDisplayState{Enabled: enabled}
	if enabled {
		var err error
		next.Accounts, err = loadAccountDisplayDetails()
		if err != nil {
			return err
		}
	}
	return r.saveAccountDisplay(next)
}
func (r *runtime) saveAccountDisplay(next accountDisplayState) error {
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.state.AccountDisplay
	r.state.AccountDisplay = next
	if err := writeState(r.cfg.StatePath, cloneState(r.state)); err != nil {
		r.state.AccountDisplay = old
		return err
	}
	return nil
}
func (r *runtime) refreshAccountDisplay() {
	r.displayMu.Lock()
	defer r.displayMu.Unlock()
	if !r.accountDisplay().Enabled {
		return
	}
	details, err := loadAccountDisplayDetails()
	if err != nil {
		return
	}
	if err = r.saveAccountDisplay(accountDisplayState{Enabled: true, Accounts: details}); err != nil {
		r.setLastError("account_display_write_failed")
	}
}
func displayAccountName(id string, info accountDisplayDetail) string {
	if info.Name == "" {
		return id
	}
	plan := map[string]string{"plus": "Plus", "team": "Team", "pro": "Pro", "business": "Business", "enterprise": "Enterprise", "edu": "Edu", "free": "Free"}[info.AccountType]
	if plan == "" {
		plan = "未知套餐"
	}
	return fmt.Sprintf("%s · %s（%s）", info.Name, plan, id)
}
func (r *runtime) displayNotification(body string) string {
	d := r.accountDisplay()
	if !d.Enabled {
		return body
	}
	pairs := make([]string, 0, len(d.Accounts)*2)
	for id, info := range d.Accounts {
		pairs = append(pairs, id, displayAccountName(id, info))
	}
	if len(pairs) == 0 {
		return body
	}
	return strings.NewReplacer(pairs...).Replace(body)
}
