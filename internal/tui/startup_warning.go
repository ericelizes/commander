package tui

import "errors"

func applyCommanderStartupWarning(warn string, svc *Services, apply func(CommanderLiveData)) {
	if warn == "" {
		return
	}
	if svc != nil && svc.Store != nil {
		return
	}
	apply(CommanderLiveData{LoadErr: errors.New(warn)})
}
