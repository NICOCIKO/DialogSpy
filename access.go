package main

import (
	"strconv"
	"strings"
)

type AccessControl struct {
	adminSet map[int64]struct{}
	adminIDs []int64
}

func NewAccessControl(primaryAdminID int64, rawAdminIDs string) *AccessControl {
	ac := &AccessControl{
		adminSet: make(map[int64]struct{}),
	}

	ac.addAdmin(primaryAdminID)

	for _, part := range strings.Split(rawAdminIDs, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ac.addAdmin(id)
	}

	return ac
}

func (ac *AccessControl) addAdmin(id int64) {
	if id <= 0 {
		return
	}
	if _, exists := ac.adminSet[id]; exists {
		return
	}
	ac.adminSet[id] = struct{}{}
	ac.adminIDs = append(ac.adminIDs, id)
}

func (ac *AccessControl) IsAdmin(id int64) bool {
	_, exists := ac.adminSet[id]
	return exists
}

func (ac *AccessControl) PrimaryAdminID() int64 {
	if len(ac.adminIDs) == 0 {
		return 0
	}
	return ac.adminIDs[0]
}

func (ac *AccessControl) AdminIDs() []int64 {
	out := make([]int64, len(ac.adminIDs))
	copy(out, ac.adminIDs)
	return out
}
